// Package muster is Countersign's side of its one dependency. Muster hands
// out work and records every result. Countersign reaches it only through the
// Queue interface here.
//
// Muster does not exist yet, and its wire contract is not written down, so
// there is no HTTP client in this package. StandIn is an in-memory Muster
// that follows the specification, and it is what Countersign runs against
// today.
package muster

import (
	"context"

	"github.com/ClearedStack/countersign/internal/record"
)

// Lease is one item handed to Countersign. ID is the identifier the lease
// was issued under. Payload is the item as Muster sent it, not yet checked.
type Lease struct {
	ID      string
	Payload []byte
}

// Queue is the set of calls a worker makes to Muster. The method names are
// the ones the naming registry gives for Muster's worker interface.
//
// An error that carries the public code E_PARSE means Muster refused the
// call cleanly and the item is unchanged. Any other error means the call may
// or may not have arrived, and is treated as E_TIMEOUT: the caller keeps the
// record and tries again. Sending a record again is always safe.
type Queue interface {
	// Lease returns up to max items that are waiting for the owner and that
	// no worker holds, urgent lane first.
	Lease(ctx context.Context, max int) ([]Lease, error)
	// Heartbeat extends the leases on the given items and returns the ones
	// still held.
	Heartbeat(ctx context.Context, ids []string) ([]string, error)
	// Complete records a decision.
	Complete(ctx context.Context, rec record.Record) error
	// Fail reports an item that could not be read. Its state does not change.
	Fail(ctx context.Context, rec record.Record) error
	// NeedsSubtool pauses an item Countersign has no rule for.
	NeedsSubtool(ctx context.Context, rec record.Record) error
}
