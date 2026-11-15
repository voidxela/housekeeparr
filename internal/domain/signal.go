// Package domain holds the normalized facts that adapters report and that
// the policy, findings, and interface layers consume. It contains no I/O.
package domain

import (
	"encoding/json"
	"fmt"
)

// SignalStatus says whether a fact was actually observed.
//
// A failed or unsupported read must never be interpreted as a zero value:
// "unknown" peers is not zero peers, and "unsupported" privacy is not public.
type SignalStatus string

const (
	// Known means the value was read from the source in this observation.
	Known SignalStatus = "known"
	// Unknown means the source normally exposes the value but did not
	// provide a usable one in this observation.
	Unknown SignalStatus = "unknown"
	// Unsupported means the source (or this adapter/version) does not
	// expose the value at all.
	Unsupported SignalStatus = "unsupported"
)

// Signal is a value together with its evidence status.
type Signal[T any] struct {
	Status SignalStatus
	Value  T
	// Note optionally explains an unknown/unsupported status or qualifies
	// the meaning of a known value.
	Note string
}

// KnownValue returns a known signal.
func KnownValue[T any](v T) Signal[T] { return Signal[T]{Status: Known, Value: v} }

// UnknownValue returns an unknown signal with an explanation.
func UnknownValue[T any](note string) Signal[T] { return Signal[T]{Status: Unknown, Note: note} }

// UnsupportedValue returns an unsupported signal with an explanation.
func UnsupportedValue[T any](note string) Signal[T] {
	return Signal[T]{Status: Unsupported, Note: note}
}

// Get returns the value and whether it is known.
func (s Signal[T]) Get() (T, bool) { return s.Value, s.Status == Known }

// IsKnown reports whether the value was observed.
func (s Signal[T]) IsKnown() bool { return s.Status == Known }

type signalJSON[T any] struct {
	Status SignalStatus `json:"status"`
	Value  *T           `json:"value,omitempty"`
	Note   string       `json:"note,omitempty"`
}

// MarshalJSON emits the value only when it is known, so a zero value is
// never confused with missing evidence by consumers of the JSON form.
func (s Signal[T]) MarshalJSON() ([]byte, error) {
	out := signalJSON[T]{Status: s.Status, Note: s.Note}
	if out.Status == "" {
		out.Status = Unknown
	}
	if s.Status == Known {
		v := s.Value
		out.Value = &v
	}
	return json.Marshal(out)
}

// UnmarshalJSON accepts the form produced by MarshalJSON.
func (s *Signal[T]) UnmarshalJSON(b []byte) error {
	var in signalJSON[T]
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	switch in.Status {
	case Known:
		if in.Value == nil {
			return fmt.Errorf("known signal without value")
		}
		s.Value = *in.Value
	case Unknown, Unsupported:
		var zero T
		s.Value = zero
	default:
		return fmt.Errorf("invalid signal status %q", in.Status)
	}
	s.Status = in.Status
	s.Note = in.Note
	return nil
}

// String renders the signal for human output.
func (s Signal[T]) String() string {
	if s.Status == Known {
		return fmt.Sprint(s.Value)
	}
	return string(s.Status)
}
