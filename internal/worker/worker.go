// Package worker is the lease loop that connects the inbox to Muster. It
// never decides anything. Each tick it:
//
//  1. sends the spooled results to Muster, oldest first, stopping at the
//     first one Muster gives no usable answer to;
//  2. sends a heartbeat for every lease it holds, and lets go of items whose
//     lease is no longer held;
//  3. leases until Muster has nothing more or the inbox is full, checking
//     each item in;
//  4. sends anything step 3 spooled.
//
// A decision the owner makes between ticks is sent at the start of the next.
package worker

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/store"
)

// MaxPending is the most items held for the owner at once. It bounds the
// spool directory, the memory a listing takes, and the heartbeats per tick.
const MaxPending = 256

// Interval is how often the loop runs. Muster's leases last 60 seconds, so a
// heartbeat every 20 keeps one through two missed beats.
const Interval = 20 * time.Second

// CallTimeout is how long one call to Muster may take unless set otherwise.
const CallTimeout = 10 * time.Second

// Event is one line of the worker's log. It holds a rule, the public code if
// the rule has one, and an item identifier. It has no free text.
type Event struct {
	At          string    `json:"at"`
	Rule        rule.ID   `json:"rule"`
	Code        rule.Code `json:"code,omitempty"`
	Item        string    `json:"item,omitempty"`
	Seq         uint64    `json:"seq,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

// Worker ties a store to a queue.
type Worker struct {
	Store *store.Store
	Queue muster.Queue
	Now   func() time.Time
	Log   func(Event)
	// Timeout is how long one call to Muster may take. Zero means CallTimeout.
	Timeout time.Duration
}

func (w *Worker) emit(id rule.ID, itemID string, seq uint64) {
	if w.Log == nil {
		return
	}
	ev := Event{At: record.Stamp(w.Now()), Rule: id, Item: itemID, Seq: seq}
	if info, _ := rule.Lookup(id); info.Code != "" {
		e := rule.Fail(id, itemID)
		ev.Code, ev.Item, ev.Fingerprint = e.Code(), e.Item, e.Fingerprint()
	}
	w.Log(ev)
}

// timedOut logs a call Muster did not answer. When the worker itself is being
// stopped the call was cut short on purpose, and that is not Muster's failure.
func (w *Worker) timedOut(ctx context.Context, id rule.ID, itemID string, seq uint64) {
	if ctx.Err() == nil {
		w.emit(id, itemID, seq)
	}
}

func (w *Worker) call(ctx context.Context, fn func(context.Context) error) error {
	limit := w.Timeout
	if limit <= 0 {
		limit = CallTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return fn(ctx)
}

// Tick runs the loop once. It reports whether Muster answered every call.
func (w *Worker) Tick(ctx context.Context) bool {
	if !w.flush(ctx) || !w.hold(ctx) {
		return false
	}
	spooled, ok := w.lease(ctx)
	if ok && spooled {
		return w.flush(ctx)
	}
	return ok
}

// flush sends spooled results in order.
//
//   - 200: the result leaves the spool as delivered.
//   - The lease is not held: a decision is kept, out of the way, until Muster
//     offers the item again. Anything else is set aside.
//   - Any other refusal: the result is set aside in the log, marked refused,
//     and the item stays as it was.
//   - No usable answer, or "try later": the result stays at the front, and
//     everything behind it waits.
func (w *Worker) flush(ctx context.Context) bool {
	entries, err := w.Store.Outbox()
	if err != nil {
		w.emit(rule.StoreUnavailable, "", 0)
		return false
	}
	setAside := func(e store.Entry, id rule.ID) bool {
		if w.Store.Refuse(e.Seq, rule.Fail(id, e.Record.Item).Fingerprint()) != nil {
			w.emit(rule.StoreUnavailable, "", 0)
			return false
		}
		w.emit(id, e.Record.Item, e.Seq)
		return true
	}
	for _, e := range entries {
		rec := e.Record
		if e.Status == store.Unleased {
			continue // waiting for the item to be leased again
		}
		if e.Lease == "" && rec.Call != record.Release {
			// Added by hand: there is no lease to send it under.
			if !setAside(e, rule.SpoolNoLease) {
				return false
			}
			continue
		}
		err := w.call(ctx, func(ctx context.Context) error {
			switch rec.Call {
			case record.Complete:
				result, err := json.Marshal(rec)
				if err != nil {
					return muster.ErrRefused
				}
				return w.Queue.Complete(ctx, e.Lease, rec.To, result)
			case record.Fail:
				return w.Queue.Fail(ctx, e.Lease, rule.Code(rec.Code))
			case record.NeedsSubtool:
				return w.Queue.NeedsSubtool(ctx, e.Lease)
			case record.Release:
				return w.Queue.Release(ctx, rec.Item)
			}
			return muster.ErrRefused
		})
		switch {
		case err == nil:
			if w.Store.Ack(e.Seq) != nil {
				w.emit(rule.StoreUnavailable, "", 0)
				return false
			}
			w.emit(rule.SpoolAcknowledged, rec.Item, e.Seq)
		case errors.Is(err, muster.ErrLeaseNotHeld) && rec.Call == record.Complete:
			if w.Store.Unlease(e.Seq) != nil {
				w.emit(rule.StoreUnavailable, "", 0)
				return false
			}
			w.emit(rule.SpoolLeaseNotHeld, rec.Item, e.Seq)
		case errors.Is(err, muster.ErrLeaseNotHeld), errors.Is(err, muster.ErrRefused):
			if !setAside(e, rule.SpoolRefused) {
				return false
			}
		default:
			w.timedOut(ctx, rule.SpoolTimeout, rec.Item, e.Seq)
			return false
		}
	}
	return true
}

// hold sends a heartbeat for each lease and drops items whose lease is no
// longer held. Muster offers such an item again, and it comes back in.
func (w *Worker) hold(ctx context.Context) bool {
	pending, err := w.Store.Pending()
	if err != nil {
		w.emit(rule.StoreUnavailable, "", 0)
		return false
	}
	for _, h := range pending {
		if h.Lease == "" {
			continue // added by hand
		}
		err := w.call(ctx, func(ctx context.Context) error { return w.Queue.Heartbeat(ctx, h.Lease) })
		switch {
		case err == nil:
		case errors.Is(err, muster.ErrLeaseNotHeld):
			if w.Store.Drop(h.Item.ID) != nil {
				w.emit(rule.StoreUnavailable, "", 0)
				return false
			}
			w.emit(rule.LeaseLost, h.Item.ID, 0)
		case errors.Is(err, muster.ErrRefused):
			// Refused for a reason that is not this lease, such as the
			// credential. Nothing is known about the lease, so nothing is
			// dropped.
			w.emit(rule.LeaseRefused, "", 0)
			return false
		default:
			w.timedOut(ctx, rule.LeaseTimeout, "", 0)
			return false
		}
	}
	return true
}

// lease takes on items until Muster has no more or the inbox is full. It
// reports whether anything is in the spool to send, and whether Muster
// answered.
func (w *Worker) lease(ctx context.Context) (spooled, ok bool) {
	pending, err := w.Store.Pending()
	if err != nil {
		w.emit(rule.StoreUnavailable, "", 0)
		return false, false
	}
	// At most MaxPending leases a tick, so a Muster that keeps offering
	// something unusable cannot hold the loop.
	held := len(pending)
	for tries := 0; tries < MaxPending && held < MaxPending; tries++ {
		var body []byte
		err := w.call(ctx, func(ctx context.Context) error {
			var err error
			body, err = w.Queue.Lease(ctx)
			return err
		})
		switch {
		case err == nil:
		case errors.Is(err, muster.ErrRefused), errors.Is(err, muster.ErrLeaseNotHeld):
			w.emit(rule.LeaseRefused, "", 0)
			return spooled, false
		default:
			w.timedOut(ctx, rule.LeaseTimeout, "", 0)
			return spooled, false
		}
		if body == nil {
			return spooled, true // nothing more: wait for the next tick
		}
		l, perr := muster.DecodeLease(body)
		if perr == nil {
			it, ierr := l.Item()
			if ierr == nil {
				arrival, err := w.Store.Accept(l.Token, it)
				if err != nil {
					w.emit(rule.StoreUnavailable, "", 0)
					return spooled, false
				}
				switch {
				case arrival.Resent:
					spooled = true
					w.emit(rule.SpoolResent, it.ID, 0)
					continue
				case arrival.Stale:
					w.emit(rule.SpoolStale, it.ID, 0)
				}
				held++
				w.emit(rule.IntakeAccept, it.ID, 0)
				continue
			}
			perr = ierr
		}
		// Broken or unfamiliar: tell Muster, show the owner nothing.
		if l.Token != "" {
			rec, err := w.Store.Report(l.Token, perr, w.Now())
			if err != nil {
				w.emit(rule.StoreUnavailable, "", 0)
				return spooled, false
			}
			spooled = spooled || rec != nil
		}
		w.emit(perr.Rule, perr.Item, 0)
	}
	return spooled, true
}
