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

// ErrRefused means Muster answered and turned the call down: the move is not
// allowed, the request was not accepted as written, or the caller may not
// make it. Sending the same call again will not help.
var ErrRefused = errors.New("muster: the call was refused")

// ErrLeaseNotHeld means Muster turned the call down because the lease is
// unknown, has ended, or was taken again. The step itself is still there
// and will be offered again.
var ErrLeaseNotHeld = errors.New("muster: the lease was lost")

// ErrUnreachable means the call may or may not have arrived: no answer, an
// answer that was not understood, or an answer that says try later. It
// carries no detail, because the detail of a network failure names an
// address.
var ErrUnreachable = errors.New("muster: no usable answer")

// Queue is the set of calls Countersign makes to Muster: the five worker
// calls, and the release call that only Countersign may make.
//
// Every method returns nil, ErrRefused, ErrLeaseNotHeld or ErrUnreachable.
// Repeating a call that ended a lease is safe: Muster gives the same answer
// and does nothing twice.
type Queue interface {
	// Lease asks for the next step that is Countersign's. It returns the
	// body of Muster's answer, not yet checked, or nil when there is nothing
	// to lease.
	Lease(ctx context.Context) ([]byte, error)
	// Heartbeat extends a lease.
	Heartbeat(ctx context.Context, lease string) error
	// Complete ends a lease with a decision: the state the item moves to,
	// and the decision record as the result.
	Complete(ctx context.Context, lease string, state item.State, result []byte) error
	// Fail ends a lease without a result. With E_PARSE Muster stops work on
	// the item and holds it. With E_TIMEOUT the step is offered again.
	Fail(ctx context.Context, lease string, code rule.Code) error
	// NeedsSubtool asks Muster to pause an item Countersign has no rule for.
	NeedsSubtool(ctx context.Context, lease string) error
	// Release carries the owner's decision to let a stopped item run again.
	// It names the item, not a lease: a stopped item is never leased.
	Release(ctx context.Context, id string) error
}
