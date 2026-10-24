// Package jobs is the queue-agnostic vocabulary for background work.
//
// Modules define job argument types and handler functions using only this
// package; they never import the queue implementation (River today, see
// ADR-0010). The composition root registers handlers with the adapter in
// internal/platform/riverjobs.
//
// Jobs are delivered at least once: handlers MUST be idempotent.
package jobs

import "context"

// Args is the payload of a job. Kind must be unique and stable forever,
// because it is persisted with queued jobs.
type Args interface {
	Kind() string
}

// Handler processes one job.
type Handler[T Args] func(ctx context.Context, args T) error
