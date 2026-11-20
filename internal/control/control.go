// Package control lets headless CLI mutations reach the running worker
// (SPEC §10: route commands through the owner or refuse clearly). The
// worker owns the state store's single-writer lock; a CLI command that
// finds it held sends its request over an owner-only Unix socket in the
// state directory instead. The worker runs it through the same service
// (actor, audit trail, preview/confirm semantics and execution mutex) as
// its web interface.
//
// A request is answered once, after it has finished. If the connection is
// lost before the answer arrives, the outcome is unknown: the client says
// so and never retries; the operator checks the finding or the action
// journal.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/voidxela/housekeeparr/internal/action"
	"github.com/voidxela/housekeeparr/internal/app"
	"github.com/voidxela/housekeeparr/internal/policy"
	"github.com/voidxela/housekeeparr/internal/store"
)

// SocketName is the socket's file name in the state directory.
const SocketName = "control.sock"

// Path returns the socket path for a state directory.
func Path(stateDir string) string { return filepath.Join(stateDir, SocketName) }

// Request is one mutation. Fields not used by Op are ignored.
type Request struct {
	Op string `json:"op"`
	// Finding or action ID.
	ID       int64         `json:"id,omitempty"`
	Note     string        `json:"note,omitempty"`
	To       string        `json:"to,omitempty"`       // review: succeeded|failed|cancelled
	Duration time.Duration `json:"duration,omitempty"` // snooze
	Workflow string        `json:"workflow,omitempty"` // pause, resume
	Ref      string        `json:"ref,omitempty"`      // attempts reset
	Confirm  bool          `json:"confirm,omitempty"`  // act, restore-ack
}

// Ops lists the operations the worker accepts.
var Ops = []string{"act", "ack", "reopen", "snooze", "review", "pause", "resume", "attempts-reset", "restore-ack"}

// Response is the worker's answer.
type Response struct {
	OK bool `json:"ok"`
	// Error is why the request was not carried out, or how it ended.
	Error string `json:"error,omitempty"`
	// Refused: nothing was sent externally (prerequisites, policy, a
	// conflicting lock, or a journal failure before any request).
	Refused bool `json:"refused,omitempty"`
	// NotFound: the finding or action does not exist.
	NotFound bool `json:"not_found,omitempty"`
	// Act's preview and result.
	Preview *app.ActionPreview `json:"preview,omitempty"`
	Result  *action.Result     `json:"result,omitempty"`
}

// Server answers control requests for the worker.
type Server struct {
	svc  *app.Service
	ln   net.Listener
	path string
	done chan struct{}
}

// Listen opens the socket. A socket file left by a worker that is gone (no
// listener answers) is removed first; one that answers belongs to a live
// process and is an error. The socket is owner-only (0600).
func Listen(svc *app.Service, stateDir string) (*Server, error) {
	path := Path(stateDir)
	if _, err := os.Lstat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return nil, fmt.Errorf("control socket %s is in use by another process", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale control socket: %w", err)
		}
	}
	old := umask(0o177)
	ln, err := net.Listen("unix", path)
	umask(old)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	s := &Server{svc: svc, ln: ln, path: path, done: make(chan struct{})}
	go s.serve()
	return s, nil
}

// Close stops accepting requests and removes the socket. A request in
// progress finishes (its journal writes do not depend on the connection).
func (s *Server) Close() error {
	err := s.ln.Close()
	<-s.done
	_ = os.Remove(s.path)
	return err
}

func (s *Server) serve() {
	defer close(s.done)
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(c)
	}
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	if !peerIsOwner(c) {
		_ = json.NewEncoder(c).Encode(Response{Error: "refused: the requesting process does not belong to this worker's user", Refused: true})
		return
	}
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	var req Request
	if err := json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&req); err != nil {
		_ = json.NewEncoder(c).Encode(Response{Error: "malformed request: " + err.Error(), Refused: true})
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	// The request outlives the connection: a client giving up must not stop
	// a workflow between its journaled writes.
	resp := s.do(context.Background(), req)
	_ = json.NewEncoder(c).Encode(resp)
}

func (s *Server) do(ctx context.Context, r Request) Response {
	actor := app.ActorCLI
	var err error
	resp := Response{}
	switch r.Op {
	case "act":
		if !r.Confirm {
			return Response{Error: "act requires confirmation of the previewed effects", Refused: true}
		}
		resp.Preview, resp.Result, err = s.svc.Act(ctx, r.ID, actor)
	case "ack":
		_, err = s.svc.Acknowledge(ctx, r.ID, actor)
	case "reopen":
		_, err = s.svc.Reopen(ctx, r.ID, actor)
	case "snooze":
		_, err = s.svc.Snooze(ctx, r.ID, r.Duration, actor)
	case "review":
		err = s.svc.Review(ctx, r.ID, store.IntentState(r.To), actor, r.Note)
	case "pause":
		err = s.svc.PauseWorkflow(ctx, policy.Workflow(r.Workflow), actor, r.Note)
	case "resume":
		err = s.svc.ResumeWorkflow(ctx, policy.Workflow(r.Workflow), actor, r.Note)
	case "attempts-reset":
		err = s.svc.ResetAttempts(ctx, r.Ref, actor, r.Note)
	case "restore-ack":
		if !r.Confirm {
			return Response{Error: "restore-ack requires confirmation", Refused: true}
		}
		err = s.svc.AcknowledgeRestore(ctx, actor)
	default:
		return Response{Error: fmt.Sprintf("unknown operation %q", r.Op), Refused: true}
	}
	var ref *action.RefusedError
	switch {
	case errors.Is(err, app.ErrNotFound):
		resp.Error, resp.NotFound = err.Error(), true
	case errors.As(err, &ref), errors.Is(err, app.ErrNoExecutor):
		resp.Error, resp.Refused = err.Error(), true
	case errors.Is(err, action.ErrJournal):
		resp.Error, resp.Refused = err.Error(), !action.MaybeSent(err)
	case err != nil:
		resp.Error = err.Error()
	default:
		resp.OK = true
	}
	return resp
}

// ErrNoWorker means no worker answers on the socket: nothing was sent.
var ErrNoWorker = errors.New("no running worker answers on the control socket")

// ErrLost means the connection ended after the request was sent and before
// the answer: the outcome is unknown and the request is not retried.
var ErrLost = errors.New("the worker connection was lost before it answered; the request may or may not have been carried out. " +
	"Check the finding or `housekeeparr actions` before trying again")

// Send delivers one request to the worker and waits for its answer.
func Send(ctx context.Context, stateDir string, r Request) (Response, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", Path(stateDir))
	if err != nil {
		return Response{}, fmt.Errorf("%w (%v); nothing was sent", ErrNoWorker, err)
	}
	defer c.Close()
	if err := json.NewEncoder(c).Encode(r); err != nil {
		// The request may have been read in part; an incomplete request
		// is refused as malformed, a complete one runs.
		return Response{}, ErrLost
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetReadDeadline(dl)
	}
	var resp Response
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return Response{}, ErrLost
	}
	return resp, nil
}
