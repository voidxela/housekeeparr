// Package httpx provides the bounded, sanitizing HTTP transport shared by
// all HTTP-based adapters: timeouts, reverse-proxy base paths, response size
// limits, strict TLS verification, and classified errors.
package httpx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/voidxela/housekeeparr/internal/adapter"
	"github.com/voidxela/housekeeparr/internal/redact"
)

// Options configures a Client.
type Options struct {
	Timeout          time.Duration // per request, including body read
	MaxResponseBytes int64
	CAFile           string // optional extra CA bundle (PEM); verification is never disabled
	CookieJar        bool
	UserAgent        string
}

// Client is a small wrapper over net/http.
type Client struct {
	base     *url.URL
	http     *http.Client
	maxBytes int64
	ua       string
}

// New builds a client for baseURL, which may include a reverse-proxy path.
func New(baseURL string, opts Options) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid base url %q", redact.URL(baseURL))
	}
	if u.User != nil || u.RawQuery != "" {
		return nil, errors.New("base url must not contain credentials or a query string")
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxResponseBytes <= 0 {
		opts.MaxResponseBytes = 16 << 20
	}
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if opts.CAFile != "" {
		pem, err := os.ReadFile(opts.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read ca file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("ca file contains no PEM certificates")
		}
		tlsCfg.RootCAs = pool
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:       tlsCfg,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: opts.Timeout,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
	}
	hc := &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
		// Never follow redirects: a redirect usually means an
		// authentication proxy or a wrong base path, and following it could
		// send credentials to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if opts.CookieJar {
		jar, _ := cookiejar.New(nil)
		hc.Jar = jar
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "Housekeeparr"
	}
	return &Client{base: u, http: hc, maxBytes: opts.MaxResponseBytes, ua: ua}, nil
}

// BaseDisplay returns the sanitized base URL.
func (c *Client) BaseDisplay() string { return redact.URL(c.base.String()) }

// URL joins an API path (beginning with "/") to the base URL.
func (c *Client) URL(path string, q url.Values) string {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	if q != nil {
		u.RawQuery = q.Encode()
	}
	return u.String()
}

// Request describes one call.
type Request struct {
	Op     string // adapter operation name for errors
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   io.Reader
	// Write marks requests whose timeout could mean an external effect
	// occurred. Login requests are not writes.
	Write bool
}

// Response is a fully-read, size-bounded response.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do executes the request and classifies failures. Non-2xx statuses are
// returned as *adapter.Error.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, r.Method, c.URL(r.Path, r.Query), r.Body)
	if err != nil {
		return nil, &adapter.Error{Class: adapter.ErrPermanent, Op: r.Op, Message: "could not build request", Err: err}
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("User-Agent", c.ua)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.transportError(ctx, r, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, c.transportError(ctx, r, err)
	}
	if int64(len(body)) > c.maxBytes {
		return nil, &adapter.Error{Class: adapter.ErrPermanent, Op: r.Op,
			Message: fmt.Sprintf("response exceeded the %d byte limit", c.maxBytes)}
	}
	out := &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return out, nil
	}
	statusErr := StatusError(r.Op, resp.StatusCode, resp.Header, body)
	if r.Write && resp.StatusCode == http.StatusRequestTimeout {
		statusErr.Class = adapter.ErrUncertain
	}
	return out, statusErr
}

func (c *Client) transportError(ctx context.Context, r Request, err error) error {
	msg := "request failed"
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		msg = "request cancelled"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		msg = "request timed out"
	default:
		var ue *url.Error
		if errors.As(err, &ue) && ue.Err != nil {
			err = ue.Err // drop the URL from the message
		}
		var certErr *tls.CertificateVerificationError
		if errors.As(err, &certErr) {
			return &adapter.Error{Class: adapter.ErrPermanent, Op: r.Op,
				Message: "TLS certificate verification failed: " + redact.String(certErr.Err.Error())}
		}
		msg = redact.Truncate(redact.String(err.Error()), 200)
	}
	class := adapter.ErrTransient
	if r.Write && msg == "request timed out" {
		class = adapter.ErrUncertain
	}
	_ = ctx
	return &adapter.Error{Class: class, Op: r.Op, Message: msg}
}

// StatusError classifies a non-2xx HTTP status.
func StatusError(op string, status int, h http.Header, body []byte) *adapter.Error {
	e := &adapter.Error{Op: op, HTTPStatus: status}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		e.Class = adapter.ErrUnauthorized
		e.Message = fmt.Sprintf("HTTP %d: credentials rejected", status)
	case status == http.StatusTooManyRequests:
		e.Class = adapter.ErrRateLimited
		e.Message = "HTTP 429: rate limited"
		e.RetryAfter = parseRetryAfter(h.Get("Retry-After"), time.Now())
	case status >= 300 && status < 400:
		e.Class = adapter.ErrUnauthorized
		e.Message = fmt.Sprintf("HTTP %d redirect (authentication proxy or wrong base path?)", status)
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented:
		e.Class = adapter.ErrUnsupported
		e.Message = fmt.Sprintf("HTTP %d: endpoint not available (unsupported version or wrong base path?)", status)
	case status >= 500:
		e.Class = adapter.ErrTransient
		e.Message = fmt.Sprintf("HTTP %d: server error", status)
		if ra := parseRetryAfter(h.Get("Retry-After"), time.Now()); ra > 0 {
			e.RetryAfter = ra
		}
	default:
		e.Class = adapter.ErrPermanent
		e.Message = fmt.Sprintf("HTTP %d", status)
	}
	if snippet := strings.TrimSpace(redact.Truncate(redact.String(string(body)), 160)); snippet != "" && !strings.HasPrefix(snippet, "<") {
		e.Message += ": " + snippet
	}
	return e
}

func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// Malformed builds a permanent error for an undecodable response.
func Malformed(op string, err error) error {
	return &adapter.Error{Class: adapter.ErrPermanent, Op: op,
		Message: "malformed response: " + redact.Truncate(redact.String(err.Error()), 160)}
}
