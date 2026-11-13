package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/store"
)

var at = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

// slow is a Muster that never answers a record call until the caller gives up.
type slow struct{ muster.Queue }

func (slow) Complete(ctx context.Context, _ record.Record) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestACallThatHangsIsATimeoutAndTheRecordStays(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.Intake("", []byte(`{"id":"PA-0001","kind":"candidate","state":"scored","lane":"normal","title":"A made-up tool","score":82,"recommendation":"approved"}`), at)
	if err != nil || in.Err != nil {
		t.Fatal(err, in.Err)
	}
	if _, err := st.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Approve}, at); err != nil {
		t.Fatal(err)
	}
	var events []Event
	w := &Worker{
		Store:   st,
		Queue:   slow{},
		Now:     func() time.Time { return at },
		Log:     func(ev Event) { events = append(events, ev) },
		Timeout: 20 * time.Millisecond,
	}
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

func TestEventsNeverNameAnAddress(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue, err := muster.NewStandIn(muster.Seed{Timeouts: 3}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	w := &Worker{Store: st, Queue: queue, Now: func() time.Time { return at }, Log: func(ev Event) {
		b, _ := json.Marshal(ev)
		lines = append(lines, string(b))
	}}
	for i := 0; i < 3; i++ {
		w.Tick(context.Background())
	}
	if len(lines) != 3 {
		t.Fatalf("events: %v", lines)
	}
	for _, l := range lines {
		if strings.Contains(l, muster.LeakMarker) || strings.Contains(l, "dial") {
			t.Errorf("an event leaks the failure's own text: %s", l)
		}
		if !strings.Contains(l, `"code":"E_TIMEOUT"`) {
			t.Errorf("event without the public code: %s", l)
		}
	}
}

func TestStoppingTheWorkerIsNotATimeout(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queue, err := muster.NewStandIn(muster.Seed{}, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	w := &Worker{Store: st, Queue: queue, Now: func() time.Time { return at }, Log: func(ev Event) { events = append(events, ev) }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Tick(ctx)
	if len(events) != 0 {
		t.Errorf("a stopped worker logged %+v", events)
	}
}
