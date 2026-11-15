// Package redact removes credentials and sensitive identifiers from strings
// before they are logged, stored as evidence, or shown in an interface.
package redact

import (
	"net/url"
	"regexp"
	"strings"
)

// URL returns a display-safe form of a URL: scheme, host, and path only.
// User info, query strings, and fragments are removed.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[unparseable url]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.Path = Passkeys(u.Path)
	u.RawPath = ""
	return u.String()
}

var (
	// key=value pairs whose values are secrets.
	secretParam = regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|passkey|token|access[_-]?token|auth|password|passwd|pass|sid|session|secret|key|torrent_pass|authkey|uid)=([^&\s"';]+)`)
	// Header-like secrets.
	secretHeader = regexp.MustCompile(`(?i)\b(x-api-key|authorization|cookie|set-cookie)\s*[:=]\s*[^\r\n]+`)
	// URL user info: scheme://user:pass@host
	userInfo = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`)
	// Long hex/base32-ish path segments commonly used as tracker passkeys.
	passkeySegment = regexp.MustCompile(`(?i)/([a-z0-9]{24,})(/|$)`)
	// Any URL: drop the query and redact passkey-like path segments.
	anyURL = regexp.MustCompile(`(?i)\b(https?|udp|wss?)://[^\s"'<>]+`)
)

// Passkeys replaces long opaque path segments with a placeholder.
func Passkeys(path string) string {
	return passkeySegment.ReplaceAllString(path, "/[redacted]$2")
}

// String sanitizes free text such as upstream error messages.
func String(s string) string {
	s = secretHeader.ReplaceAllString(s, "$1: [redacted]")
	s = userInfo.ReplaceAllString(s, "$1[redacted]@")
	s = anyURL.ReplaceAllStringFunc(s, func(m string) string {
		q := ""
		if i := strings.IndexByte(m, '?'); i >= 0 {
			m, q = m[:i], "?[redacted]"
		}
		// Keep scheme://host intact; only the path can carry passkeys.
		if i := strings.Index(m, "://"); i >= 0 {
			if j := strings.IndexByte(m[i+3:], '/'); j >= 0 {
				return m[:i+3+j] + Passkeys(m[i+3+j:]) + q
			}
		}
		return m + q
	})
	s = secretParam.ReplaceAllString(s, "$1=[redacted]")
	return s
}

// Truncate shortens s to at most n runes, marking truncation.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
