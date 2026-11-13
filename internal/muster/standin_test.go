package muster

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
)

var at = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

const (
	scoredItem   = `{"id":"PA-0001","kind":"candidate","state":"scored","lane":"normal","title":"A made-up tool","score":70,"recommendation":"parked"}`
	screenedItem = `{"id":"PA-0002","kind":"candidate","state":"screened-out","lane":"normal","title":"A made-up tool","screen_reason":"Fails a gate."}`
	proposalItem = `{"id":"EP-0001","kind":"proposal","state":"pending_approval","lane":"normal","type":"DATA","target":"catalog/sample/description","body":"Fix the spelling."}`
)

func standIn(t *testing.T) *StandIn {
	t.Helper()
	s, err := NewStandIn(Seed{
		Ownership: []string{"catalog/sample/description"},
		Items:     []json.RawMessage{json.RawMessage(scoredItem), json.RawMessage(screenedItem), json.RawMessage(proposalItem)},
	}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func decision(t *testing.T, payload string, d decide.Decision) record.Record {
	t.Helper()
	it, perr := item.Parse("", []byte(payload))
	if perr != nil {
		t.Fatal(perr)
	}
	res, perr := decide.Apply(it, d, at)
	if perr != nil {
		t.Fatal(perr)
	}
	return res.Record
}

func isRefusal(err error) bool {
	var e *rule.Error
	return errors.As(err, &e) && e.Code() == rule.Parse
}

// The stand-in checks a decision for itself. It does not trust that
// Countersign already did.
func TestStandInRefusesWhatTheSpecificationSaysMusterRefuses(t *testing.T) {
	ctx := context.Background()
	good := decision(t, screenedItem, decide.Decision{Item: "PA-0002", Outcome: item.Override, Reason: "Reshaped."})

	forge := func(change func(*record.Record)) record.Record {
		r := good
		change(&r)
		return r.Seal()
	}
	tampered := good
	tampered.Reason = "Edited after sealing."

	bad := map[string]record.Record{
		"override with no written reason":       forge(func(r *record.Record) { r.Reason = "" }),
		"override with a blank reason":          forge(func(r *record.Record) { r.Reason = "  \n " }),
		"override not marked as one":            forge(func(r *record.Record) { r.Override = false }),
		"wrong starting state":                  forge(func(r *record.Record) { r.From = item.Scored }),
		"move the table does not allow":         forge(func(r *record.Record) { r.Outcome, r.To = item.Approve, item.Approved }),
		"destination that is not the outcome's": forge(func(r *record.Record) { r.To = item.Approved }),
		"item Muster does not have":             forge(func(r *record.Record) { r.Item = "PA-0404" }),
		"wrong kind":                            forge(func(r *record.Record) { r.Kind = item.Proposal }),
		"wrong lane":                            forge(func(r *record.Record) { r.Lane = item.Urgent }),
		"content changed after sealing":         tampered,
		"against the recommendation, no reason": func() record.Record {
			r := decision(t, scoredItem, decide.Decision{Item: "PA-0001", Outcome: item.Approve, Reason: "x"})
			r.Reason = ""
			return r.Seal()
		}(),
	}
	for name, rec := range bad {
		s := standIn(t)
		if err := s.Complete(ctx, rec); !isRefusal(err) {
			t.Errorf("%s: got %v, want a clean refusal", name, err)
		}
		snap := s.Snapshot()
		if len(snap.Ledger) != 0 {
			t.Errorf("%s: a refused record reached the ledger", name)
		}
		for _, it := range snap.Items {
			if it.Status == StatusClosed {
				t.Errorf("%s: a refused record changed %s", name, it.ID)
			}
		}
	}

	s := standIn(t)
	if err := s.Fail(ctx, good); !isRefusal(err) {
		t.Errorf("a decision sent as a failure: %v", err)
	}
	if err := s.Complete(ctx, good); err != nil {
		t.Errorf("a good decision: %v", err)
	}
	if err := s.Complete(ctx, good); err != nil {
		t.Errorf("the same decision again must be confirmed again: %v", err)
	}
	if n := len(s.Snapshot().Ledger); n != 1 {
		t.Errorf("the ledger holds %d records", n)
	}
	other := decision(t, screenedItem, decide.Decision{Item: "PA-0002", Outcome: item.Override, Reason: "A different reason."})
	if err := s.Complete(ctx, other); !isRefusal(err) {
		t.Errorf("a second, different decision on a closed item: %v", err)
	}
}

func TestDeclinedProposalDispatchesNothing(t *testing.T) {
	ctx := context.Background()
	s := standIn(t)
	if err := s.Complete(ctx, decision(t, proposalItem, decide.Decision{Item: "EP-0001", Outcome: item.Decline})); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if len(snap.Dispatched) != 0 {
		t.Errorf("a declined proposal dispatched work: %+v", snap.Dispatched)
	}

	s = standIn(t)
	if err := s.Complete(ctx, decision(t, proposalItem, decide.Decision{Item: "EP-0001", Outcome: item.Approve})); err != nil {
		t.Fatal(err)
	}
	snap = s.Snapshot()
	if len(snap.Dispatched) != 1 || snap.Dispatched[0] != (Dispatch{Item: "EP-0001", Worker: "bellbuoy", Lane: item.Urgent}) {
		t.Errorf("dispatched: %+v", snap.Dispatched)
	}
}

func TestLeaseRespectsMaxAndLaneOrder(t *testing.T) {
	seed := Seed{Items: []json.RawMessage{
		json.RawMessage(`{"id":"PA-0001","lane":"backfill"}`),
		json.RawMessage(`{"id":"PA-0002","lane":"normal"}`),
		json.RawMessage(`{"id":"PA-0003","lane":"urgent"}`),
	}}
	s, err := NewStandIn(seed, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Lease(context.Background(), 2)
	if err != nil || len(got) != 2 || got[0].ID != "PA-0003" || got[1].ID != "PA-0002" {
		t.Fatalf("lease: %+v %v", got, err)
	}
	got, _ = s.Lease(context.Background(), 5)
	if len(got) != 1 || got[0].ID != "PA-0001" {
		t.Fatalf("second lease: %+v", got)
	}
}

func TestSeedMustBeUsable(t *testing.T) {
	clock := func() time.Time { return at }
	for name, seed := range map[string]Seed{
		"no id":        {Items: []json.RawMessage{json.RawMessage(`{"kind":"candidate"}`)}},
		"duplicate id": {Items: []json.RawMessage{json.RawMessage(scoredItem), json.RawMessage(scoredItem)}},
		"unsafe id":    {Raw: []RawItem{{ID: "../x", Payload: "{}"}}},
	} {
		if _, err := NewStandIn(seed, clock); !errors.Is(err, ErrSeed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseSeed([]byte(`{"items":[],"surprise":1}`)); !errors.Is(err, ErrSeed) {
		t.Errorf("unknown field in a seed: %v", err)
	}
}
