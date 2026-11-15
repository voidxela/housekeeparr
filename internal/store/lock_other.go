//go:build !unix

package store

import "errors"

// Lock is unavailable on this platform.
type Lock struct{}

// LockedError reports that another process owns the state store.
type LockedError struct {
	Path string
	PID  int
}

func (e *LockedError) Error() string { return "state store is owned by another process" }

// AcquireLock refuses to run as a writer on platforms without a validated
// OS-level ownership lock. Windows and others need explicit validation
// before being supported.
func AcquireLock(string) (*Lock, error) {
	return nil, errors.New("single-writer locking is not implemented on this platform; Housekeeparr currently supports Linux and other Unix systems")
}

// Release is a no-op.
func (l *Lock) Release() error { return nil }
