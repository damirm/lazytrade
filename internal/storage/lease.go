package storage

import "context"

type AgentLease interface {
	Acquire(context.Context, string) error
	Release(context.Context) error
}
