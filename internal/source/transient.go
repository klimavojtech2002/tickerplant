package source

import "errors"

// ErrTransient marks a source failure that is worth retrying: the network or the venue
// was briefly unavailable, but nothing says the next attempt will fail too. Anything not
// marked is permanent and is returned to the caller at once.
var ErrTransient = errors.New("transient source failure")

type transientError struct{ err error }

func (t *transientError) Error() string        { return t.err.Error() }
func (t *transientError) Unwrap() error        { return t.err }
func (t *transientError) Is(target error) bool { return target == ErrTransient }

// Transient marks err as retryable. The cause stays reachable with errors.Is and
// errors.As, so existing checks for a specific sentinel keep working.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return &transientError{err: err}
}

// IsTransient reports whether err, or anything it wraps, was marked with Transient.
func IsTransient(err error) bool { return errors.Is(err, ErrTransient) }
