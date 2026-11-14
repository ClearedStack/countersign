package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/store"
)

var at = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

const (
	scoredStep   = `{"kind":"item","id":"PA-0001","state":"scored","lane":"normal","results":{"assay":{"total":82,"recommendation":"Build"}}}`
	proposalStep = `{"kind":"proposal","id":"EP-0001","state":"pending_approval","lane":"urgent","proposal":{"target":"catalog/sample/description","repository":"clearedstack.com","file":"src/data/sample.json","edit_kind":"DATA","change":"Fix the spelling.","reason":""}}`
)

func standIn(t *testing.T, seed muster.Seed, steps ...string) *muster.StandIn {
	t.Helper()
	seed.Ownership = []string{"catalog/sample/description"}
	for _, s := range steps {
		seed.Steps = append(seed.Steps, json.RawMessage(s))
	}
	s, err := muster.NewStandIn(seed, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rules(events []Event) string {
	var out []string
	for _, ev := range events {
		out = append(out, string(ev.Rule))
	}
	return strings.Join(out, " ")
}

// hang is a Muster that takes a lease and then never answers.
type hang struct{ muster.Queue }

func (hang) Complete(ctx context.Context, _ string, _ item.State, _ []byte) error {
	<-ctx.Done()
	return muster.ErrUnreachable
}

func TestACallThatHangsIsATimeoutAndTheRecordStays(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	w := &Worker{
		Store:   st,
		Queue:   hang{standIn(t, muster.Seed{}, scoredStep)},
		Now:     func() time.Time { return at },
		Log:     func(ev Event) { events = append(events, ev) },
		Timeout: 20 * time.Millisecond,
	}
	w.Tick(context.Background())
	if _, err := st.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Approve}, at); err != nil {
		t.Fatal(err)
	}
	events = nil
	start := time.Now()
	if w.Tick(context.Background()) {
		t.Error("the tick reported that Muster answered")
	}
	if time.Since(start) > 2*time.Second {
		t.Error("the call was not cut off")
	}
	if len(events) != 1 || events[0].Rule != rule.SpoolTimeout || events[0].Code != rule.Timeout || events[0].Item != "PA-0001" {
		t.Fatalf("events: %+v", events)
	}
	if out, _ := st.Outbox(); len(out) != 1 {
		t.Error("the record left the spool without being confirmed")
	}
}

func TestStoppingTheWorkerIsNotATimeout(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	w := &Worker{Store: st, Queue: standIn(t, muster.Seed{}, scoredStep), Now: func() time.Time { return at }, Log: func(ev Event) { events = append(events, ev) }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Tick(ctx)
	if len(events) != 0 {
		t.Errorf("a stopped worker logged %+v", events)
	}
}

// The lease loop over real HTTP: the worker, the client, a fake Muster
// server and the stand-in behind it.
func TestLeaseLoopOverHTTP(t *testing.T) {
	const token = "made-up-token-for-countersign"
	s := standIn(t, muster.Seed{}, scoredStep, proposalStep)
	down := false
	fake := muster.NewFakeServer(s, token)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down {
			http.Error(w, "made-up outage at 192.0.2.7", http.StatusBadGateway)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()

	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := muster.NewHTTP(srv.URL, tokenPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	var lines []string
	w := &Worker{Store: st, Queue: client, Now: func() time.Time { return at }, Log: func(ev Event) {
		events = append(events, ev)
		b, _ := json.Marshal(ev)
		lines = append(lines, string(b))
	}}
	tick := func() string {
		events = nil
		w.Tick(context.Background())
		return rules(events)
	}

	// Lease until 204: the urgent proposal first.
	if got := tick(); got != "intake.accept intake.accept" {
		t.Fatalf("first tick: %s", got)
	}
	pending, _ := st.Pending()
	if len(pending) != 2 || pending[0].Item.ID != "EP-0001" || pending[0].Lease != "EP-0001.1" || pending[1].Lease != "PA-0001.2" {
		t.Fatalf("inbox: %+v", pending)
	}
	// Nothing new: heartbeats only.
	if got := tick(); got != "" {
		t.Errorf("idle tick: %s", got)
	}

	// The owner decides while Muster is down. The results wait in the spool.
	if _, err := st.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Approve}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Decide(decide.Decision{Item: "EP-0001", Outcome: item.Decline, Reason: "Not a mistake."}, at); err != nil {
		t.Fatal(err)
	}
	down = true
	if got := tick(); got != "spool.timeout" {
		t.Errorf("tick while Muster is down: %s", got)
	}
	if out, _ := st.Outbox(); len(out) != 2 {
		t.Fatalf("the spool holds %d results", len(out))
	}
	down = false
	if got := tick(); got != "spool.acknowledged spool.acknowledged" {
		t.Errorf("tick after Muster is back: %s", got)
	}
	if out, _ := st.Outbox(); len(out) != 0 {
		t.Errorf("the spool was not emptied after the 200s: %+v", out)
	}

	snap := s.Snapshot()
	if len(snap.Ledger) != 2 || snap.Ledger[0].ID != "PA-0001" || snap.Ledger[0].State != "approved" ||
		snap.Ledger[1].ID != "EP-0001" || snap.Ledger[1].State != "declined" || snap.Ledger[1].Result.Reason != "Not a mistake." {
		t.Errorf("ledger: %+v", snap.Ledger)
	}
	for _, l := range lines {
		if strings.Contains(l, "192.0.2.7") || strings.Contains(l, srv.Listener.Addr().String()) || strings.Contains(l, token) {
			t.Errorf("an event leaks an address or the token: %s", l)
		}
	}
}

// From Muster's own examples: the worker is handed the release step in
// lease-release.json, the owner decides, and what the worker sends must be
// the request in complete-release.json or complete-keep.json, exactly.
func TestReleaseDecisionIsSentAsTheContractExample(t *testing.T) {
	type example struct {
		Request struct {
			Path string          `json:"path"`
			Body json.RawMessage `json:"body"`
		} `json:"request"`
		Expect struct {
			Status int             `json:"status"`
			Body   json.RawMessage `json:"body"`
		} `json:"expect"`
	}
	load := func(name string) example {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("../../testdata/contract", name))
		if err != nil {
			t.Fatal(err)
		}
		var ex example
		if err := json.Unmarshal(raw, &ex); err != nil {
			t.Fatal(err)
		}
		return ex
	}
	lease := load("lease-release.json")

	for file, outcome := range map[string]item.Outcome{"complete-release.json": item.LetGo, "complete-keep.json": item.Keep} {
		want := load(file)
		var wantBody bytes.Buffer
		if err := json.Compact(&wantBody, want.Request.Body); err != nil {
			t.Fatal(err)
		}
		leases, sent := 0, ""
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			switch r.URL.Path {
			case "/v1/lease":
				leases++
				if leases > 1 {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				w.Write(lease.Expect.Body)
			case "/v1/complete":
				sent = string(b)
				w.Write(want.Expect.Body)
			default:
				w.Write([]byte(`{}`))
			}
		}))
		tokenPath := filepath.Join(t.TempDir(), "token")
		if err := os.WriteFile(tokenPath, []byte("made-up-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		client, err := muster.NewHTTP(srv.URL, tokenPath)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		var events []Event
		w := &Worker{Store: st, Queue: client, Now: func() time.Time { return at }, Log: func(ev Event) { events = append(events, ev) }}

		w.Tick(context.Background())
		pending, _ := st.Pending()
		if len(pending) != 1 || pending[0].Item.Kind != item.Release || pending[0].Item.HeldWorker != "sextant" || pending[0].Lease != "release:PA-0001.2" {
			t.Fatalf("%s: inbox after the lease: %+v (%s)", file, pending, rules(events))
		}
		if _, err := st.Decide(decide.Decision{Item: "PA-0001", Outcome: outcome, Reason: "A made-up note kept in the local log."}, at); err != nil {
			t.Fatal(err)
		}
		events = nil
		w.Tick(context.Background())
		if sent != wantBody.String() {
			t.Errorf("%s: the worker sent\n  %s\nand the contract's example is\n  %s", file, sent, wantBody.String())
		}
		if got := rules(events); got != "spool.acknowledged" {
			t.Errorf("%s: %s", file, got)
		}
		srv.Close()
	}
}
