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
	scoredStep   = `{"kind":"item","id":"PA-0001","state":"scored","lane":"normal","results":{"assay":{"total":70,"recommendation":"Park"}}}`
	screenedStep = `{"kind":"item","id":"PA-0002","state":"screened_out","lane":"normal","results":{"assay":{"gates":[{"name":"One job","pass":false,"reason":"It does three jobs."}]}}}`
	proposalStep = `{"kind":"proposal","id":"EP-0001","state":"pending_approval","lane":"normal","proposal":{"target":"catalog/sample/description","repository":"clearedstack.com","file":"src/data/sample.json","edit_kind":"DATA","change":"Fix the spelling.","reason":""}}`
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

func standIn(t *testing.T, c *clock, steps ...string) *StandIn {
	t.Helper()
	seed := Seed{Ownership: []string{"catalog/sample/description"}}
	for _, s := range steps {
		seed.Steps = append(seed.Steps, json.RawMessage(s))
	}
	s, err := NewStandIn(seed, c.Now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// take leases the next step and returns the lease and the item.
func take(t *testing.T, s *StandIn) (Lease, item.Item) {
	t.Helper()
	body, err := s.Lease(context.Background())
	if err != nil || body == nil {
		t.Fatalf("lease: %s %v", body, err)
	}
	l, perr := DecodeLease(body)
	if perr != nil {
		t.Fatal(perr)
	}
	it, perr := l.Item()
	if perr != nil {
		t.Fatal(perr)
	}
	return l, it
}

func decided(t *testing.T, it item.Item, d decide.Decision) record.Record {
	t.Helper()
	d.Item = it.ID
	res, perr := decide.Apply(it, d, at)
	if perr != nil {
		t.Fatal(perr)
	}
	return res.Record
}

func send(s *StandIn, lease string, rec record.Record) error {
	b, _ := json.Marshal(rec)
	return s.Complete(context.Background(), lease, rec.To, b)
}

// The stand-in checks a decision for itself. It does not trust that
// Countersign already did.
func TestStandInRefusesWhatMusterWouldRefuse(t *testing.T) {
	forge := func(good record.Record, change func(*record.Record)) record.Record {
		r := good
		change(&r)
		return r.Seal()
	}
	cases := map[string]func(good record.Record) record.Record{
		"override with no written reason": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.Reason = "" })
		},
		"override with a blank reason": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.Reason = "  \n " })
		},
		"override not marked as one": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.Override = false })
		},
		"wrong starting state": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.From = item.Scored })
		},
		"a move the table does not have": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.Outcome, r.To = item.Approve, item.Approved })
		},
		"a record for another item": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.Item = "PA-0404" })
		},
		"wrong kind": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.Kind = item.Proposal })
		},
		"wrong lane": func(g record.Record) record.Record {
			return forge(g, func(r *record.Record) { r.Lane = item.Urgent })
		},
		"content changed after sealing": func(g record.Record) record.Record {
			g.Reason = "Edited after sealing."
			return g
		},
	}
	for name, bad := range cases {
		s := standIn(t, &clock{at}, screenedStep)
		l, it := take(t, s)
		good := decided(t, it, decide.Decision{Outcome: item.Override, Reason: "Reshaped."})
		if err := send(s, l.Token, bad(good)); !errors.Is(err, ErrRefused) {
			t.Errorf("%s: got %v, want a refusal", name, err)
		}
		snap := s.Snapshot()
		if len(snap.Ledger) != 0 || snap.Items[0].State != "screened_out" || snap.Items[0].Status != StatusLeased {
			t.Errorf("%s: a refused decision changed something: %+v", name, snap)
		}
		// The item is still there to be decided properly.
		if err := send(s, l.Token, good); err != nil {
			t.Errorf("%s: the good decision afterwards: %v", name, err)
		}
	}

	// The state named in the call must be the one in the record.
	s := standIn(t, &clock{at}, screenedStep)
	l, it := take(t, s)
	good := decided(t, it, decide.Decision{Outcome: item.Override, Reason: "Reshaped."})
	b, _ := json.Marshal(good)
	if err := s.Complete(context.Background(), l.Token, item.Approved, b); !errors.Is(err, ErrRefused) {
		t.Errorf("state and record disagree: %v", err)
	}
	if err := s.Complete(context.Background(), l.Token, item.Observed, []byte(`{"note":"made-up"}`)); !errors.Is(err, ErrRefused) {
		t.Errorf("a result that is not a decision record: %v", err)
	}
	if err := s.Complete(context.Background(), "PA-0002.99", item.Observed, b); !errors.Is(err, ErrRefused) {
		t.Errorf("a lease that was never given: %v", err)
	}
}

func TestAgainstTheRecommendationNeedsAReasonAtMusterToo(t *testing.T) {
	s := standIn(t, &clock{at}, scoredStep)
	l, it := take(t, s)
	rec := decided(t, it, decide.Decision{Outcome: item.Approve, Reason: "x"})
	rec.Reason = ""
	if err := send(s, l.Token, rec.Seal()); !errors.Is(err, ErrRefused) {
		t.Errorf("got %v", err)
	}
}

func TestRepeatingTheCallThatEndedALease(t *testing.T) {
	s := standIn(t, &clock{at}, screenedStep)
	l, it := take(t, s)
	good := decided(t, it, decide.Decision{Outcome: item.Override, Reason: "Reshaped."})
	for i := 0; i < 3; i++ {
		if err := send(s, l.Token, good); err != nil {
			t.Fatalf("send %d: %v", i+1, err)
		}
	}
	if n := len(s.Snapshot().Ledger); n != 1 {
		t.Errorf("the ledger holds %d entries", n)
	}
	if err := s.Fail(context.Background(), l.Token, rule.Parse); !errors.Is(err, ErrRefused) {
		t.Errorf("a different call on an ended lease: %v", err)
	}
	if err := s.Heartbeat(context.Background(), l.Token); !errors.Is(err, ErrRefused) {
		t.Errorf("heartbeat on an ended lease: %v", err)
	}
}

func TestLeasesRunOutAndALateResultStillCounts(t *testing.T) {
	c := &clock{at}
	s := standIn(t, c, scoredStep)
	ctx := context.Background()
	l, it := take(t, s)

	c.now = c.now.Add(50 * time.Second)
	if err := s.Heartbeat(ctx, l.Token); err != nil {
		t.Fatalf("heartbeat inside the lease: %v", err)
	}
	c.now = c.now.Add(50 * time.Second) // 100s after leasing, 50s after the heartbeat
	if err := s.Heartbeat(ctx, l.Token); err != nil {
		t.Fatalf("the heartbeat did not extend the lease: %v", err)
	}
	c.now = c.now.Add(61 * time.Second)
	if err := s.Heartbeat(ctx, l.Token); !errors.Is(err, ErrRefused) {
		t.Fatalf("heartbeat after the lease ran out: %v", err)
	}
	// Nobody has taken the step again, so the late result is kept.
	if err := send(s, l.Token, decided(t, it, decide.Decision{Outcome: item.Park, Blocker: "Waiting."})); err != nil {
		t.Fatalf("late result: %v", err)
	}

	// Once the step is taken again, the old lease is refused.
	s = standIn(t, c, scoredStep)
	old, it := take(t, s)
	c.now = c.now.Add(61 * time.Second)
	fresh, _ := take(t, s)
	if fresh.Token == old.Token || fresh.Attempt != 2 {
		t.Fatalf("second lease: %+v", fresh)
	}
	rec := decided(t, it, decide.Decision{Outcome: item.Park, Blocker: "Waiting."})
	if err := send(s, old.Token, rec); !errors.Is(err, ErrRefused) {
		t.Errorf("the lost lease was honoured: %v", err)
	}
	if err := send(s, fresh.Token, rec); err != nil {
		t.Errorf("the new lease: %v", err)
	}
}

func TestFailAndNeedsSubtool(t *testing.T) {
	ctx := context.Background()
	c := &clock{at}

	s := standIn(t, c, scoredStep)
	l, _ := take(t, s)
	if err := s.Fail(ctx, l.Token, rule.Parse); err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); snap.Items[0].Status != StatusHeld || snap.Items[0].State != "scored" {
		t.Errorf("after E_PARSE: %+v", snap.Items[0])
	}
	// The item itself is not offered again. The owner is given a release
	// step for it instead.
	r, held := take(t, s)
	if r.Token != "release:PA-0001.2" || held.Kind != item.Release || held.State != item.Scored || held.HeldWorker != "countersign" || held.HeldCode != "E_PARSE" {
		t.Fatalf("release step: %+v %+v", r, held)
	}
	if body, err := s.Lease(ctx); err != nil || body != nil {
		t.Errorf("something else was offered: %s %v", body, err)
	}
	release := func(state item.State, result string) error {
		return s.Complete(ctx, r.Token, state, []byte(result))
	}
	for name, err := range map[string]error{
		"another state":       release(item.Approved, `{"decision":"release"}`),
		"no decision":         release(item.Scored, `{}`),
		"an unknown decision": release(item.Scored, `{"decision":"approve"}`),
		"another field":       release(item.Scored, `{"decision":"release","reason":"made-up"}`),
		"the decision record": release(item.Scored, `{"contract":"v1","call":"complete","outcome":"release"}`),
		"paused":              s.NeedsSubtool(ctx, r.Token),
		"failed with E_PARSE": s.Fail(ctx, r.Token, rule.Parse),
	} {
		if !errors.Is(err, ErrRefused) {
			t.Errorf("release step, %s: got %v, want a refusal", name, err)
		}
	}
	if err := release(item.Scored, `{"decision":"release"}`); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := release(item.Scored, `{"decision":"release"}`); err != nil {
		t.Errorf("the same release again: %v", err)
	}
	if err := release(item.Scored, `{"decision":"keep"}`); !errors.Is(err, ErrRefused) {
		t.Errorf("a different decision on an ended release step: %v", err)
	}
	// Released: the item's own step is offered again, in the urgent lane.
	again, _ := take(t, s)
	if again.ID != "PA-0001" || again.Kind != "item" || again.Lane != "urgent" || again.Attempt != 2 {
		t.Errorf("after release: %+v", again)
	}

	// Kept: the release step ends and nothing offers the item again.
	s = standIn(t, c, scoredStep)
	l, _ = take(t, s)
	if err := s.Fail(ctx, l.Token, rule.Parse); err != nil {
		t.Fatal(err)
	}
	r, _ = take(t, s)
	if err := s.Complete(ctx, r.Token, item.Scored, []byte(`{"decision":"keep"}`)); err != nil {
		t.Fatalf("keep: %v", err)
	}
	if body, err := s.Lease(ctx); err != nil || body != nil {
		t.Errorf("a kept item was offered again: %s %v", body, err)
	}

	s = standIn(t, c, scoredStep)
	l, _ = take(t, s)
	if err := s.Fail(ctx, l.Token, rule.Timeout); err != nil {
		t.Fatal(err)
	}
	if again, _ := take(t, s); again.ID != "PA-0001" || again.Attempt != 2 {
		t.Errorf("after E_TIMEOUT the step should be offered again: %+v", again)
	}

	s = standIn(t, c, scoredStep, proposalStep)
	l, _ = take(t, s)
	if err := s.Fail(ctx, l.Token, rule.NeedsSubtool); !errors.Is(err, ErrRefused) {
		t.Errorf("fail with a code that is not its own: %v", err)
	}
	if err := s.NeedsSubtool(ctx, l.Token); !errors.Is(err, ErrRefused) {
		t.Errorf("a scored item waits for a person, not for a rule: %v", err)
	}
	p, _ := take(t, s)
	if err := s.NeedsSubtool(ctx, p.Token); !errors.Is(err, ErrRefused) {
		t.Errorf("a proposal cannot be paused: %v", err)
	}
}

func TestProposalsApprovedAndDeclined(t *testing.T) {
	s := standIn(t, &clock{at}, proposalStep)
	l, it := take(t, s)
	if err := send(s, l.Token, decided(t, it, decide.Decision{Outcome: item.Decline})); err != nil {
		t.Fatal(err)
	}
	if snap := s.Snapshot(); len(snap.Dispatched) != 0 || snap.Items[0].State != "declined" {
		t.Errorf("declined: %+v", snap)
	}

	s = standIn(t, &clock{at}, proposalStep)
	l, it = take(t, s)
	if err := send(s, l.Token, decided(t, it, decide.Decision{Outcome: item.Approve})); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	if len(snap.Dispatched) != 1 || snap.Dispatched[0] != (Dispatch{Item: "EP-0001", Worker: "bellbuoy", Lane: item.Urgent}) || snap.Items[0].Lane != "urgent" {
		t.Errorf("approved: %+v", snap)
	}
}

func TestOneLeaseAtATimeUrgentFirst(t *testing.T) {
	s := standIn(t, &clock{at},
		`{"kind":"item","id":"PA-0001","state":"scored","lane":"backfill"}`,
		`{"kind":"item","id":"PA-0002","state":"scored","lane":"normal"}`,
		`{"kind":"item","id":"PA-0003","state":"scored","lane":"urgent"}`,
		`{"kind":"item","id":"PA-0004","state":"scored","lane":"normal"}`)
	var got []string
	for {
		body, err := s.Lease(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if body == nil {
			break
		}
		l, perr := DecodeLease(body)
		if perr != nil {
			t.Fatal(perr)
		}
		got = append(got, l.Token)
	}
	want := []string{"PA-0003.1", "PA-0002.2", "PA-0004.3", "PA-0001.4"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestSeedMustBeUsable(t *testing.T) {
	now := (&clock{at}).Now
	for name, seed := range map[string]Seed{
		"no id":         {Steps: []json.RawMessage{json.RawMessage(`{"kind":"item"}`)}},
		"not an object": {Steps: []json.RawMessage{json.RawMessage(`"PA-0001"`)}},
		"duplicate id":  {Steps: []json.RawMessage{json.RawMessage(scoredStep), json.RawMessage(scoredStep)}},
		"unsafe id":     {Raw: []RawStep{{ID: "../x", Body: "{}"}}},
	} {
		if _, err := NewStandIn(seed, now); !errors.Is(err, ErrSeed) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := ParseSeed([]byte(`{"steps":[],"surprise":1}`)); !errors.Is(err, ErrSeed) {
		t.Errorf("unknown field in a seed: %v", err)
	}
}
