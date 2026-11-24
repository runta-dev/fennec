package launcher

import "errors"

// NotStartedError is a launch the broker refused before it started or changed
// anything: no session, journal entry or cloud operation exists for the
// request. Its message is written for the user. Any other launch failure
// leaves the outcome unknown.
type NotStartedError struct{ Message string }

func (e *NotStartedError) Error() string { return e.Message }

// NotStarted marks err as a refusal before anything started.
func NotStarted(err error) error {
	if err == nil {
		return nil
	}
	return &NotStartedError{Message: err.Error()}
}

// IsNotStarted reports whether err is a launch refused before anything started.
func IsNotStarted(err error) bool {
	var refused *NotStartedError
	return errors.As(err, &refused)
}
