package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SecretRef points at a credential without containing it. Supported forms:
//
//	env:NAME        value of environment variable NAME
//	file:/path      contents of a file (trailing newline trimmed)
//
// Literal secrets are rejected so credentials never live in the
// configuration file itself.
type SecretRef string

// Kind returns "env", "file", or "" if malformed.
func (r SecretRef) Kind() string {
	s := string(r)
	switch {
	case strings.HasPrefix(s, "env:") && len(s) > 4:
		return "env"
	case strings.HasPrefix(s, "file:") && len(s) > 5:
		return "file"
	}
	return ""
}

// Display is safe to show: the reference itself, never the value.
func (r SecretRef) Display() string {
	if r == "" {
		return "(none)"
	}
	return string(r)
}

func (r SecretRef) validateSyntax() error {
	if r == "" {
		return nil
	}
	switch r.Kind() {
	case "env":
		name := string(r)[4:]
		for _, c := range name {
			if !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
				return fmt.Errorf("invalid environment variable name in secret reference %q", r)
			}
		}
		return nil
	case "file":
		if !filepath.IsAbs(string(r)[5:]) {
			return fmt.Errorf("secret file reference %q must use an absolute path", r)
		}
		return nil
	}
	return errors.New(`secrets must be references ("env:NAME" or "file:/absolute/path"), not literal values`)
}

// SecretWarning is a non-fatal concern about a secret source.
type SecretWarning string

// Resolve returns the secret value. Errors never contain the value.
func (r SecretRef) Resolve(lookupEnv func(string) (string, bool)) (string, []SecretWarning, error) {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	switch r.Kind() {
	case "env":
		name := string(r)[4:]
		v, ok := lookupEnv(name)
		if !ok {
			return "", nil, fmt.Errorf("environment variable %s is not set", name)
		}
		if strings.TrimSpace(v) == "" {
			return "", nil, fmt.Errorf("environment variable %s is empty", name)
		}
		return v, nil, nil
	case "file":
		path := string(r)[5:]
		fi, err := os.Stat(path)
		if err != nil {
			return "", nil, fmt.Errorf("secret file %s: %w", path, unwrapPathErr(err))
		}
		if !fi.Mode().IsRegular() {
			return "", nil, fmt.Errorf("secret file %s is not a regular file", path)
		}
		if fi.Size() > 64<<10 {
			return "", nil, fmt.Errorf("secret file %s is unexpectedly large", path)
		}
		var warns []SecretWarning
		if fi.Mode().Perm()&0o077 != 0 {
			warns = append(warns, SecretWarning(fmt.Sprintf("secret file %s is accessible by group or others (mode %04o); restrict it to 0600", path, fi.Mode().Perm())))
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", warns, fmt.Errorf("secret file %s: %w", path, unwrapPathErr(err))
		}
		v := strings.TrimRight(string(b), "\r\n")
		if strings.TrimSpace(v) == "" {
			return "", warns, fmt.Errorf("secret file %s is empty", path)
		}
		return v, warns, nil
	}
	return "", nil, errors.New("malformed secret reference")
}

func unwrapPathErr(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}
