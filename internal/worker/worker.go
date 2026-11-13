// Package worker is the loop that connects the inbox to Muster. It never
// decides anything. Each tick it:
//
//  1. sends the spooled records to Muster, oldest first, stopping at the
//     first one Muster does not confirm;
//  2. extends the leases on everything it holds, and lets go of items whose
//     lease has ended;
//  3. leases more items and checks each one in.
package worker

import (
	"context"
	"errors"
	"time"

	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/store"
)

// MaxPending is the most items held for the owner at once. It bounds the
// spool directory and the memory a listing takes.
const MaxPending = 256

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

// refused reports whether Muster turned the call down cleanly. Every other
// failure counts as a timeout, whatever its text says.
func refused(err error) bool {
	var e *rule.Error
	return errors.As(err, &e) && e.Code() == rule.Parse
}

// Tick runs the loop once. It reports whether Muster answered every call.
func (w *Worker) Tick(ctx context.Context) bool {
	return w.flush(ctx) && w.hold(ctx) && w.lease(ctx)
}

// flush sends spooled records in order. Nothing else happens in a tick until
// the spool is empty, so an item is never leased again while a decision on
// it is still on its way.
func (w *Worker) flush(ctx context.Context) bool {
	entries, err := w.Store.Outbox()
	if err != nil {
		w.emit(rule.StoreUnavailable, "", 0)
		return false
	}
	for _, e := range entries {
		rec := e.Record
		err := w.call(ctx, func(ctx context.Context) error {
			switch rec.Call {
			case record.Complete:
				return w.Queue.Complete(ctx, rec)
			case record.Fail:
				return w.Queue.Fail(ctx, rec)
			case record.NeedsSubtool:
				return w.Queue.NeedsSubtool(ctx, rec)
			}
			return rule.Fail(rule.SpoolRefused, rec.Item)
		})
		switch {
		case err == nil:
			if w.Store.Ack(e.Seq) != nil {
				w.emit(rule.StoreUnavailable, "", 0)
				return false
			}
			w.emit(rule.SpoolAcknowledged, rec.Item, e.Seq)
		case refused(err):
			fp := rule.Fail(rule.SpoolRefused, rec.Item).Fingerprint()
			if w.Store.Refuse(e.Seq, fp) != nil {
				w.emit(rule.StoreUnavailable, "", 0)
				return false
			}
			w.emit(rule.SpoolRefused, rec.Item, e.Seq)
		default:
			// Not confirmed. Keep it, and keep everything behind it in order.
			w.timedOut(ctx, rule.SpoolTimeout, rec.Item, e.Seq)
			return false
		}
	}
	return true
}

// hold extends leases and drops items whose lease has ended.
func (w *Worker) hold(ctx context.Context) bool {
	pending, err := w.Store.Pending()
	if err != nil {
		w.emit(rule.StoreUnavailable, "", 0)
		return false
	}
	if len(pending) == 0 {
		return true
	}
	ids := make([]string, 0, len(pending))
	for _, h := range pending {
		ids = append(ids, h.Item.ID)
	}
	var held []string
	err = w.call(ctx, func(ctx context.Context) error {
		var err error
		held, err = w.Queue.Heartbeat(ctx, ids)
		return err
	})
	if err != nil {
		w.timedOut(ctx, rule.LeaseTimeout, "", 0)
		return false
	}
	still := map[string]bool{}
	for _, id := range held {
		still[id] = true
	}
	for _, id := range ids {
		if still[id] {
			continue
		}
		if w.Store.Drop(id) != nil {
			w.emit(rule.StoreUnavailable, "", 0)
			return false
		}
		w.emit(rule.LeaseLost, id, 0)
	}
	return true
}

// lease takes on more items, up to MaxPending, and checks each one in.
func (w *Worker) lease(ctx context.Context) bool {
	pending, err := w.Store.Pending()
	if err != nil {
		w.emit(rule.StoreUnavailable, "", 0)
		return false
	}
	room := MaxPending - len(pending)
	if room <= 0 {
		return true
	}
	var leases []muster.Lease
	err = w.call(ctx, func(ctx context.Context) error {
		var err error
		leases, err = w.Queue.Lease(ctx, room)
		return err
	})
	if err != nil {
		w.timedOut(ctx, rule.LeaseTimeout, "", 0)
		return false
	}
	if len(leases) > room {
		leases = leases[:room]
	}
	for _, l := range leases {
		in, err := w.Store.Intake(l.ID, l.Payload, w.Now())
		if err != nil {
			w.emit(rule.StoreUnavailable, "", 0)
			return false
		}
		ref := l.ID
		if in.Err != nil {
			ref = in.Err.Item
		}
		w.emit(in.Rule, ref, 0)
	}
	return true
}
