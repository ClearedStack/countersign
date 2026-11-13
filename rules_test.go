package countersign

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/store"
	"github.com/ClearedStack/countersign/internal/worker"
)

// Every rule has saved examples under testdata/rules/<rule>/<case>/. A case
// is one of three shapes, told apart by the files it holds:
//
//	item.json [+ lease]           an item arriving; lease is the lease identifier
//	item.json + decision.json     the owner deciding on that item
//	scenario.json                 a run of the worker against the stand-in
//
// want.json is the exact output expected. Run "go test . -update" to rewrite
// the want.json files after a deliberate change, and read the difference.
var update = flag.Bool("update", false, "rewrite want.json in every saved example")

const rulesDir = "testdata/rules"

// fixedTime is when every saved example happens.
var fixedTime = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

type outcome struct {
	Rule   rule.ID        `json:"rule"`
	Code   rule.Code      `json:"code,omitempty"`
	Error  string         `json:"error,omitempty"`
	Item   *item.Item     `json:"item,omitempty"`
	Record *record.Record `json:"record,omitempty"`
}

func refusal(e *rule.Error) outcome {
	return outcome{Rule: e.Rule, Code: e.Code(), Error: e.Error()}
}

func cases(t *testing.T) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	rules, err := os.ReadDir(rulesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rules {
		if !r.IsDir() {
			t.Errorf("%s: not a directory", r.Name())
			continue
		}
		cs, err := os.ReadDir(filepath.Join(rulesDir, r.Name()))
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cs {
			out[r.Name()] = append(out[r.Name()], c.Name())
		}
	}
	return out
}

func TestEveryRuleHasASavedExample(t *testing.T) {
	have := cases(t)
	for _, info := range rule.All() {
		if len(have[string(info.ID)]) == 0 {
			t.Errorf("rule %s has no saved example", info.ID)
		}
		delete(have, string(info.ID))
	}
	for name := range have {
		t.Errorf("testdata/rules/%s matches no rule", name)
	}
}

func TestEveryRuleIsWellFormed(t *testing.T) {
	seen := map[rule.ID]bool{}
	for _, info := range rule.All() {
		if seen[info.ID] {
			t.Errorf("rule %s is listed twice", info.ID)
		}
		seen[info.ID] = true
		switch info.Code {
		case "", rule.Timeout, rule.Parse, rule.NeedsSubtool:
		default:
			t.Errorf("rule %s has a code outside the three public ones: %q", info.ID, info.Code)
		}
		if info.Text == "" {
			t.Errorf("rule %s has no plain-words meaning", info.ID)
		}
	}
}

func TestSavedExamples(t *testing.T) {
	all := cases(t)
	names := make([]string, 0, len(all))
	for name := range all {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, ruleName := range names {
		for _, caseName := range all[ruleName] {
			dir := filepath.Join(rulesDir, ruleName, caseName)
			t.Run(ruleName+"/"+caseName, func(t *testing.T) {
				var got any
				var rules []rule.ID
				switch {
				case exists(dir, "scenario.json"):
					got, rules = runScenario(t, dir)
				case exists(dir, "decision.json"):
					o := runDecision(t, dir)
					got, rules = o, []rule.ID{o.Rule}
				case exists(dir, "item.json"):
					o := runIntake(t, dir)
					got, rules = o, []rule.ID{o.Rule}
				default:
					t.Fatal("the case holds no input")
				}
				found := false
				for _, r := range rules {
					found = found || string(r) == ruleName
				}
				if !found {
					t.Errorf("the example is filed under %s but exercised %v", ruleName, rules)
				}
				compare(t, dir, got)
			})
		}
	}
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func read(t *testing.T, dir, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func compare(t *testing.T, dir string, got any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), muster.LeakMarker) {
		t.Errorf("the output names an address:\n%s", buf.String())
	}
	path := filepath.Join(dir, "want.json")
	if *update {
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no want.json: run go test . -update and check the result")
	}
	if !bytes.Equal(want, buf.Bytes()) {
		t.Errorf("output changed.\n--- want\n%s\n--- got\n%s", want, buf.Bytes())
	}
}

func runIntake(t *testing.T, dir string) outcome {
	t.Helper()
	lease := ""
	if exists(dir, "lease") {
		lease = strings.TrimSpace(string(read(t, dir, "lease")))
	}
	payload := read(t, dir, "item.json")

	// The pure check and the store must agree.
	_, perr := item.Parse(lease, payload)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	in, err := st.Intake(lease, payload, fixedTime)
	if err != nil {
		t.Fatal(err)
	}
	if (perr == nil) != (in.Err == nil) || (perr != nil && *perr != *in.Err) {
		t.Fatalf("item.Parse said %v and the store said %v", perr, in.Err)
	}
	pending, err := st.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if in.Err != nil {
		if len(pending) != 0 {
			t.Errorf("a refused or paused item reached the inbox")
		}
		o := refusal(in.Err)
		o.Record = in.Record
		return o
	}
	if len(pending) != 1 {
		t.Errorf("an accepted item is not in the inbox")
	}
	return outcome{Rule: in.Rule, Item: in.Item}
}

func runDecision(t *testing.T, dir string) outcome {
	t.Helper()
	it, perr := item.Parse("", read(t, dir, "item.json"))
	if perr != nil {
		t.Fatalf("item.json must be an item Countersign accepts: %v", perr)
	}
	d, perr := decide.Parse(read(t, dir, "decision.json"))
	if perr != nil {
		return refusal(perr)
	}
	res, perr := decide.Apply(it, d, fixedTime)
	if perr != nil {
		return refusal(perr)
	}
	return outcome{Rule: res.Rule, Record: &res.Record}
}

// A scenario seeds the stand-in and then runs steps in order.
type scenario struct {
	Muster muster.Seed `json:"muster"`
	Steps  []step      `json:"steps"`
}

type step struct {
	// Do is one of: tick, decide, add, faults, advance, break.
	Do       string          `json:"do"`
	Decision json.RawMessage `json:"decision,omitempty"` // decide
	Item     json.RawMessage `json:"item,omitempty"`     // add
	Timeouts int             `json:"timeouts,omitempty"` // faults
	LostAcks int             `json:"lost_acks,omitempty"`
	Seconds  int             `json:"seconds,omitempty"` // advance
}

type stepResult struct {
	Do     string         `json:"do"`
	Events []worker.Event `json:"events,omitempty"`
	*outcome
}

type scenarioResult struct {
	Steps   []stepResult    `json:"steps"`
	Pending []string        `json:"pending"`
	Log     []store.Entry   `json:"log"`
	Store   string          `json:"store,omitempty"`
	Muster  muster.Snapshot `json:"muster"`
}

func runScenario(t *testing.T, dir string) (scenarioResult, []rule.ID) {
	t.Helper()
	var sc scenario
	dec := json.NewDecoder(bytes.NewReader(read(t, dir, "scenario.json")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sc); err != nil {
		t.Fatalf("scenario.json: %v", err)
	}

	now := fixedTime
	clock := func() time.Time { return now }
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := muster.NewStandIn(sc.Muster, clock)
	if err != nil {
		t.Fatal(err)
	}
	var events []worker.Event
	w := &worker.Worker{Store: st, Queue: queue, Now: clock, Log: func(ev worker.Event) { events = append(events, ev) }}

	var res scenarioResult
	var rules []rule.ID
	publicOutcome := func(err error) *outcome {
		var pub *rule.Error
		if !errors.As(err, &pub) {
			t.Fatalf("an error that is not public escaped: %v", err)
		}
		o := refusal(pub)
		return &o
	}
	for _, s := range sc.Steps {
		out := stepResult{Do: s.Do}
		switch s.Do {
		case "tick":
			events = nil
			w.Tick(context.Background())
			out.Events = events
			for _, ev := range events {
				rules = append(rules, ev.Rule)
			}
		case "decide":
			d, perr := decide.Parse(s.Decision)
			if perr != nil {
				o := refusal(perr)
				out.outcome = &o
				break
			}
			r, err := st.Decide(d, now)
			if err != nil {
				out.outcome = publicOutcome(err)
				break
			}
			out.outcome = &outcome{Rule: r.Rule, Record: &r.Record}
		case "add":
			in, err := st.Intake("", s.Item, now)
			switch {
			case err != nil:
				out.outcome = publicOutcome(err)
			case in.Err != nil:
				o := refusal(in.Err)
				o.Record = in.Record
				out.outcome = &o
			default:
				out.outcome = &outcome{Rule: in.Rule, Item: in.Item}
			}
		case "faults":
			queue.SetFaults(s.Timeouts, s.LostAcks)
		case "advance":
			now = now.Add(time.Duration(s.Seconds) * time.Second)
		case "break":
			// Put a plain file where the spool's outbox directory was.
			outbox := filepath.Join(root, "outbox")
			if err := os.RemoveAll(outbox); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(outbox, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unknown step %q", s.Do)
		}
		if out.outcome != nil {
			rules = append(rules, out.outcome.Rule)
		}
		res.Steps = append(res.Steps, out)
	}

	res.Pending = []string{}
	pending, err := st.Pending()
	if err != nil {
		res.Store = publicOutcome(err).Error
	}
	for _, h := range pending {
		res.Pending = append(res.Pending, h.Item.ID)
	}
	res.Log, err = st.Log()
	if err != nil {
		res.Store = publicOutcome(err).Error
	}
	if res.Log == nil {
		res.Log = []store.Entry{}
	}
	res.Muster = queue.Snapshot()

	// Whatever happened, no half-written file may be left in the spool.
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasPrefix(d.Name(), ".tmp-") {
			t.Errorf("a temporary file was left behind")
		}
		return nil
	})
	return res, rules
}
