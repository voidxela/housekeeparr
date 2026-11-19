// Package notify turns persisted changes into notifications (SPEC §11.2)
// and delivers them to configured webhook targets.
//
// Events are queued in the store in the same transaction as the change
// they report (Enqueue), so a crash cannot lose or duplicate one; delivery
// (Deliver) runs afterwards with a persisted hourly limit per target,
// bounded retries with backoff, and an age limit. Nothing here can affect
// diagnosis or actions: a failing notification path is only recorded.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/config"
	"github.com/voidxela/housekeeparr/internal/domain"
	"github.com/voidxela/housekeeparr/internal/findings"
	"github.com/voidxela/housekeeparr/internal/redact"
	"github.com/voidxela/housekeeparr/internal/store"
)

// Event kinds.
const (
	KindOpened    = "finding.opened"
	KindResolved  = "finding.resolved"
	KindDigest    = "findings.digest"
	KindAction    = "action.outcome"       // an automatic action finished or needs review
	KindAttention = "automation.attention" // automation is stopped for a reason the user must resolve
)

func rank(s string) int {
	switch s {
	case string(domain.SeverityError):
		return 0
	case string(domain.SeverityWarning):
		return 1
	}
	return 2
}

func link(base string, id int64) string {
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + fmt.Sprintf("/findings/%d", id)
}

func body(f domain.Finding) string {
	var b strings.Builder
	b.WriteString(f.Summary)
	if len(f.Subject.Connections) > 0 {
		b.WriteString("\nAffected: " + strings.Join(f.Subject.Connections, ", "))
	}
	if len(f.Detail.NextSteps) > 0 {
		b.WriteString("\nNext step: " + f.Detail.NextSteps[0])
	}
	return redact.Truncate(redact.String(b.String()), 1000)
}

// Enqueue queues notifications for a cycle's finding changes: newly opened
// or recurred findings at or above each target's severity (several at
// once become one digest, so a shared outage cannot flood a target), and
// the resolution of a finding whose opening was delivered.
func Enqueue(tx *store.Tx, targets []config.NotifyConfig, settings config.NotificationSettings, res findings.Result, now time.Time) error {
	for _, tg := range targets {
		var opened []domain.Finding
		for _, f := range append(append([]domain.Finding{}, res.Opened...), res.Recurred...) {
			if f.State != domain.FindingSnoozed && rank(string(f.Severity)) <= rank(tg.MinSeverity) {
				opened = append(opened, f)
			}
		}
		if len(opened) > settings.DigestThreshold {
			worst := string(domain.SeverityNotice)
			var titles []string
			for i, f := range opened {
				if rank(string(f.Severity)) < rank(worst) {
					worst = string(f.Severity)
				}
				if i < settings.DigestThreshold {
					titles = append(titles, "• "+f.Title)
				}
			}
			b := fmt.Sprintf("%d findings opened at once, which often has a shared cause (a connection, tracker, or network problem).\n%s\n…and %d more.",
				len(opened), strings.Join(titles, "\n"), len(opened)-settings.DigestThreshold)
			if _, err := tx.EnqueueNotification(store.Notification{Target: tg.ID, DedupKey: fmt.Sprintf("digest|%d", now.UnixMilli()), Kind: KindDigest,
				Severity: worst, Title: fmt.Sprintf("Housekeeparr: %d new findings", len(opened)), Body: redact.Truncate(redact.String(b), 1000),
				Link: strings.TrimRight(tg.LinkBase, "/") + map[bool]string{true: "/attention", false: ""}[tg.LinkBase != ""], CreatedAt: now}); err != nil {
				return err
			}
		} else {
			for _, f := range opened {
				id := f.ID
				if _, err := tx.EnqueueNotification(store.Notification{Target: tg.ID, DedupKey: fmt.Sprintf("opened|%d", f.ID), Kind: KindOpened,
					FindingID: &id, Severity: string(f.Severity), Title: redact.String(f.Title), Body: body(f), Link: link(tg.LinkBase, f.ID), CreatedAt: now}); err != nil {
					return err
				}
			}
		}
		for _, f := range res.Resolved {
			ok, err := tx.NotifiedFinding(tg.ID, f.ID)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			id := f.ID
			if _, err := tx.EnqueueNotification(store.Notification{Target: tg.ID, DedupKey: fmt.Sprintf("resolved|%d", f.ID), Kind: KindResolved,
				FindingID: &id, Severity: string(domain.SeverityNotice), Title: "Resolved: " + redact.String(f.Title),
				Body: redact.Truncate(redact.String(f.Resolution), 1000), Link: link(tg.LinkBase, f.ID), CreatedAt: now}); err != nil {
				return err
			}
		}
	}
	return nil
}

// Report summarises one delivery pass.
type Report struct {
	Sent, Failed, Dropped, Held int
}

// Deliver sends due events, within each target's hourly limit. urls maps
// target IDs to their resolved webhook URLs; a target without one is
// skipped (its events age out).
func Deliver(ctx context.Context, st *store.Store, targets []config.NotifyConfig, settings config.NotificationSettings, urls map[string]string, client *http.Client, now time.Time) (Report, error) {
	var rep Report
	started := time.Now()
	if err := st.Write(ctx, func(tx *store.Tx) error {
		var err error
		rep.Dropped, err = tx.ExpireNotifications(now, settings.MaxAge.Duration)
		return err
	}); err != nil {
		return Report{}, err
	}
	webhookClient := http.Client{}
	if client != nil {
		webhookClient = *client
	}
	if webhookClient.Timeout <= 0 || webhookClient.Timeout > settings.RequestTimeout.Duration {
		webhookClient.Timeout = settings.RequestTimeout.Duration
	}
	webhookClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, tg := range targets {
		u := urls[tg.ID]
		if u == "" {
			continue
		}
		due, err := st.DueNotifications(ctx, tg.ID, now, 100)
		if err != nil {
			return rep, err
		}
		for i, n := range due {
			var reserved bool
			at := now.Add(time.Since(started))
			if err := st.Write(ctx, func(tx *store.Tx) error {
				var err error
				reserved, err = tx.ReserveNotificationAttempt(tg.ID, at, tg.MaxPerHour)
				return err
			}); err != nil {
				return rep, err
			}
			if !reserved {
				rep.Held += len(due) - i
				break
			}
			derr := send(ctx, &webhookClient, tg.Format, u, n)
			exhausted := derr != nil && n.Attempts+1 >= settings.MaxAttempts
			err := st.Write(ctx, func(tx *store.Tx) error {
				switch {
				case derr == nil:
					return tx.MarkNotificationSent(n.ID, now)
				case exhausted:
					return tx.DropNotification(n.ID, now, fmt.Sprintf("gave up after %d attempts: %v", settings.MaxAttempts, derr))
				default:
					d := settings.InitialBackoff.Duration
					for i := 0; i < n.Attempts && d < settings.MaxBackoff.Duration; i++ {
						d = min(d*2, settings.MaxBackoff.Duration)
					}
					return tx.MarkNotificationFailed(n.ID, now, now.Add(d), derr.Error())
				}
			})
			if err != nil {
				return rep, err
			}
			if derr == nil {
				rep.Sent++
			} else {
				rep.Failed++
				if exhausted {
					rep.Dropped++
				}
			}
		}
	}
	return rep, nil
}

// payload is the JSON body; it never carries credentials or tracker URLs.
type payload struct {
	Source    string    `json:"source"`
	Kind      string    `json:"kind"`
	Severity  string    `json:"severity"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Link      string    `json:"link,omitempty"`
	FindingID *int64    `json:"finding_id,omitempty"`
	At        time.Time `json:"at"`
}

func send(ctx context.Context, client *http.Client, format, url string, n store.Notification) error {
	n.Title = redact.String(n.Title)
	var req *http.Request
	var err error
	if format == "text" {
		text := n.Body
		if n.Link != "" {
			text += "\n" + n.Link
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(text))
		if err != nil {
			return fmt.Errorf("invalid webhook URL")
		}
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		req.Header.Set("Title", headerSafe(n.Title))
		req.Header.Set("Priority", map[string]string{"error": "high", "warning": "default", "notice": "low"}[n.Severity])
		req.Header.Set("Tags", "housekeeparr,"+n.Severity)
		if n.Link != "" {
			req.Header.Set("Click", n.Link)
		}
	} else {
		b, _ := json.Marshal(payload{Source: "housekeeparr", Kind: n.Kind, Severity: n.Severity, Title: n.Title, Body: n.Body, Link: n.Link,
			FindingID: n.FindingID, At: n.CreatedAt})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
		if err != nil {
			return fmt.Errorf("invalid webhook URL")
		}
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "housekeeparr")
	resp, err := client.Do(req)
	if err != nil {
		// Transport errors can quote the URL, which may carry a token.
		return fmt.Errorf("delivery failed (transport error)")
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("delivery refused: HTTP %d", resp.StatusCode)
	}
	return nil
}

// headerSafe keeps a header value to one printable line.
func headerSafe(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return redact.Truncate(s, 200)
}

// Event is a notification not tied to a finding change: an automatic
// action's outcome or an automation stop that needs the user.
type Event struct {
	DedupKey  string
	Kind      string
	Severity  domain.Severity
	FindingID int64
	Title     string
	Body      string
}

// EnqueueEvent queues an event for every target whose severity threshold
// it meets; a repeated dedup key is never queued twice.
func EnqueueEvent(tx *store.Tx, targets []config.NotifyConfig, ev Event, now time.Time) error {
	for _, tg := range targets {
		if rank(string(ev.Severity)) > rank(tg.MinSeverity) {
			continue
		}
		n := store.Notification{Target: tg.ID, DedupKey: ev.DedupKey, Kind: ev.Kind, Severity: string(ev.Severity), Title: redact.String(ev.Title),
			Body: redact.Truncate(redact.String(ev.Body), 1000), CreatedAt: now}
		if ev.FindingID != 0 {
			id := ev.FindingID
			n.FindingID, n.Link = &id, link(tg.LinkBase, id)
		}
		if _, err := tx.EnqueueNotification(n); err != nil {
			return err
		}
	}
	return nil
}
