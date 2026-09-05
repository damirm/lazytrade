package sqlite

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/damirm/lazytrade/internal/storage"
	"golang.org/x/sys/unix"
)

type agentLock struct {
	file  *os.File
	owner string
}

func (s *Store) Acquire(ctx context.Context, owner string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == "" {
		return errors.New("sqlite: lock owner is empty")
	}
	if s.dbPath == "" {
		return errors.New("sqlite: agent lock is unavailable for an in-memory database")
	}
	if s.lockFile != nil {
		if s.lockFile.owner == owner {
			return nil
		}
		return storage.ErrLockHeld
	}
	file, err := os.OpenFile(s.dbPath+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("sqlite: open agent lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return storage.ErrLockHeld
		}
		return fmt.Errorf("sqlite: acquire agent lock: %w", err)
	}
	s.lockFile = &agentLock{file: file, owner: owner}
	return nil
}

func (s *Store) Release(context.Context) error {
	if s.lockFile == nil {
		return nil
	}
	lock := s.lockFile
	s.lockFile = nil
	if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN); err != nil {
		_ = lock.file.Close()
		return fmt.Errorf("sqlite: release agent lock: %w", err)
	}
	if err := lock.file.Close(); err != nil {
		return fmt.Errorf("sqlite: close agent lock: %w", err)
	}
	return nil
}
