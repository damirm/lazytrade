package storage

import "errors"

var (
	ErrNotFound        = errors.New("storage: not found")
	ErrConflict        = errors.New("storage: conflict")
	ErrAlreadyExists   = errors.New("storage: already exists")
	ErrVersionConflict = errors.New("storage: version conflict")
	ErrLockHeld        = errors.New("storage: agent lock held")
)
