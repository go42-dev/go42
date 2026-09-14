package domain

import "errors"

var (
	ErrEntityNotFound = errors.New("entity not found")
	ErrInvalidInput   = errors.New("invalid input")
)
