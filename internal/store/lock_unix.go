//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Lock is an exclusive advisory lock held for the life of the process. The
// kernel releases it automatically if the process dies, so ownership never
// depends on expiring timestamps.
type Lock struct{ f *os.File }

// LockedError reports that another process owns the state store.
type LockedError struct {
	Path string
	PID  int
}

func (e *LockedError) Error() string {
	who := "another Housekeeparr process"
	if e.PID > 0 {
		who = fmt.Sprintf("another Housekeeparr process (pid %d)", e.PID)
	}
	return fmt.Sprintf("state store is owned by %s; only one writer may run. Stop it, or use read-only commands (status, findings, downloads)", who)
}

// AcquireLock takes the exclusive lock at path without blocking.
func AcquireLock(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		defer f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			buf := make([]byte, 32)
			n, _ := f.ReadAt(buf, 0)
			pid, _ := strconv.Atoi(strings.TrimSpace(string(buf[:n])))
			return nil, &LockedError{Path: path, PID: pid}
		}
		return nil, fmt.Errorf("lock state store: %w", err)
	}
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
		_ = f.Sync()
	}
	return &Lock{f: f}, nil
}

// Release drops the lock.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = l.f.Truncate(0)
	err := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	cerr := l.f.Close()
	l.f = nil
	return errors.Join(err, cerr)
}
