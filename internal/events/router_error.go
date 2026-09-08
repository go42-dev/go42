package events

import "errors"

type permanentError struct {
	err error
}

func (e *permanentError) Error() string {
	return e.err.Error()
}

func (e *permanentError) Unwrap() error {
	return e.err
}

func Permanent(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := errors.AsType[*permanentError](err); ok {
		return err
	}

	return &permanentError{err: err}
}
