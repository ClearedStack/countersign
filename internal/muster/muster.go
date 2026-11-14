// Package muster is Countersign's side of its one dependency. Muster hands
// out work and records every result. Countersign reaches it only through the
// Queue interface here, which is Muster's five worker calls.
//
// Two things stand behind the interface: HTTP, the client for a real Muster,
// and StandIn, an in-memory Muster for running and testing alone.
package muster

import (
	"context"
	"errors"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
)

// ContractVersion is sent in every request body.
const ContractVersion = "v1"

// ErrRefused means Muster answered and turned the call down: the lease is
// not held, the move is not allowed, or the request was not accepted as
// written. Sending the same call again will not help.
var ErrRefused = errors.New("muster: the call was refused")

// ErrUnreachable means the call may or may not have arrived: no answer, an
// answer that was not understood, or an answer that says try later. It is
// the only other error a Queue returns, and it carries no detail, because
// the detail of a network failure names an address.
var ErrUnreachable = errors.New("muster: no usable answer")

// Queue is the set of calls a worker makes to Muster.
//
// Every method returns nil, ErrRefused or ErrUnreachable. Repeating a call
// that Muster already applied is safe: Muster gives the same answer and does
// nothing twice.
type Queue interface {
	// Lease asks for the next step that is Countersign's. It returns the
	// body of Muster's answer, not yet checked, or nil when there is nothing
	// to lease.
	Lease(ctx context.Context) ([]byte, error)
	// Heartbeat extends a lease. ErrRefused means the lease is no longer held.
	Heartbeat(ctx context.Context, lease string) error
	// Complete ends a lease with a decision: the state the item moves to,
	// and the decision record as the result.
	Complete(ctx context.Context, lease string, state item.State, result []byte) error
	// Fail ends a lease without a result. With E_PARSE Muster stops work on
	// the item and holds it. With E_TIMEOUT the step is offered again.
	Fail(ctx context.Context, lease string, code rule.Code) error
	// NeedsSubtool asks Muster to pause an item Countersign has no rule for.
	NeedsSubtool(ctx context.Context, lease string) error
}
