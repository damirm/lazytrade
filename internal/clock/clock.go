package clock

import (
	"errors"
	"sync"
	"time"
)

var ErrTimeBackwards = errors.New("clock cannot move backwards")

type Clock interface {
	Now() time.Time
}

type MutableClock interface {
	Clock
	AdvanceTo(time.Time) error
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type FixedClock struct {
	now time.Time
}

func NewFixed(t time.Time) FixedClock {
	return FixedClock{now: t.UTC()}
}

func (c FixedClock) Now() time.Time {
	return c.now
}

type VirtualClock struct {
	mu  sync.RWMutex
	now time.Time
}

func NewVirtual(t time.Time) *VirtualClock {
	return &VirtualClock{now: t.UTC()}
}

func (c *VirtualClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *VirtualClock) AdvanceTo(t time.Time) error {
	next := t.UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	if next.Before(c.now) {
		return ErrTimeBackwards
	}
	c.now = next
	return nil
}
