package main

import "errors"

var (
	ErrNotFound       = errors.New("request not found")
	ErrUnknownStatus  = errors.New("unknown status")
	ErrReasonRequired = errors.New("rejection reason is required")
	ErrSameStatus     = errors.New("status is already the same")
)
