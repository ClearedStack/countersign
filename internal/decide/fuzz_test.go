package decide_test

import (
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
)

var fuzzTime = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

func assertDecisionError(t *testing.T, err *rule.Error) {
	t.Helper()
	info, ok := rule.Lookup(err.Rule)
	if !ok {
		t.Fatalf("unknown rule %q", err.Rule)
	}
	if info.Code != rule.Parse {
		t.Fatalf("unexpected decision code %q for rule %q", info.Code, err.Rule)
	}
	if err.Item != "" && !rule.SafeID(err.Item) {
		t.Fatalf("unsafe item ID %q in error", err.Item)
	}
	if len(err.Fingerprint()) != 16 {
		t.Fatalf("unexpected fingerprint %q", err.Fingerprint())
	}
}

func sampleItems() []item.Item {
	score := 82
	return []item.Item{
		{
			ID:             "PA-0001",
			Kind:           item.Candidate,
			State:          item.Scored,
			Lane:           item.Normal,
			Title:          "Sample rate checker",
			Score:          &score,
			Recommendation: item.Approved,
		},
		{
			ID:           "PA-0002",
			Kind:         item.Candidate,
			State:        item.ScreenedOut,
			Lane:         item.Normal,
			Title:        "Sample suite",
			ScreenReason: "one_job: Too broad.",
		},
		{
			ID:       "EP-0001",
			Kind:     item.Proposal,
			State:    item.PendingApproval,
			Lane:     item.Urgent,
			Target:   "catalog/sample/description",
			EditKind: "DATA",
			Change:   "Fix spelling.",
		},
	}
}

func FuzzDecideParseAndApply(f *testing.F) {
	seeds := []string{
		`{"item":"PA-0001","outcome":"approve"}`,
		`{"item":"PA-0001","outcome":"link","tool":"sample-tool","reason":"Already exists."}`,
		`{"item":"PA-0001","outcome":"merge","parent":"sample-parent","reason":"Fold scope."}`,
		`{"item":"PA-0001","outcome":"park","blocker":"Waiting on rule.","reason":"Defer."}`,
		`{"item":"PA-0001","outcome":"reject","reason":"Out of scope."}`,
		`{"item":"PA-0002","outcome":"override","reason":"Narrowed to one job."}`,
		`{"item":"EP-0001","outcome":"approve"}`,
		`{"item":"EP-0001","outcome":"decline","reason":"Not accurate."}`,
		`{"item":"../bad","outcome":"approve"}`,
		`{}`,
	}
	for i, s := range seeds {
		f.Add([]byte(s), uint8(i), "Sample reason")
	}

	samples := sampleItems()
	outcomes := []item.Outcome{
		item.Approve, item.Link, item.Merge, item.Park, item.Reject, item.Override, item.Decline, "unknown",
	}

	f.Fuzz(func(t *testing.T, raw []byte, pick uint8, text string) {
		it := samples[int(pick)%len(samples)]

		if d, perr := decide.Parse(raw); perr != nil {
			assertDecisionError(t, perr)
		} else {
			if !rule.SafeID(d.Item) || d.Outcome == "" {
				t.Fatalf("Parse accepted unsafe ID or empty outcome: %+v", d)
			}
			checkApply(t, it, d)
		}

		synth := decide.Decision{
			Item:    it.ID,
			Outcome: outcomes[int(pick)%len(outcomes)],
			Reason:  text,
		}
		switch synth.Outcome {
		case item.Link:
			synth.Tool = text
		case item.Merge:
			synth.Parent = text
		case item.Park:
			synth.Blocker = text
		}
		checkApply(t, it, synth)
	})
}

func checkApply(t *testing.T, it item.Item, d decide.Decision) {
	t.Helper()
	res, perr := decide.Apply(it, d, fuzzTime)
	if perr != nil {
		assertDecisionError(t, perr)
		return
	}
	rec := res.Record
	if !rec.Sealed() {
		t.Fatalf("accepted record is not sealed: %+v", rec)
	}
	if len(rec.Reason) > item.MaxReason || !item.ValidText(rec.Reason, item.MaxReason) {
		t.Fatalf("accepted record has invalid reason (%d bytes)", len(rec.Reason))
	}
	if !item.ValidText(rec.Blocker, item.MaxText) ||
		!item.ValidLine(rec.Tool, item.MaxLine) ||
		!item.ValidLine(rec.Parent, item.MaxLine) {
		t.Fatalf("accepted record has invalid target field: %+v", rec)
	}
	if rec.Override && item.Blank(rec.Reason) {
		t.Fatalf("override accepted without reason: %+v", rec)
	}
}

func FuzzRelease(f *testing.F) {
	f.Add("PA-0001", "Fixed upstream.")
	f.Add("PA-10000", "")
	f.Add("EP-0001", "Proposal cannot be released.")
	f.Add("../bad", "Path traversal.")

	f.Fuzz(func(t *testing.T, id, reason string) {
		res, perr := decide.Release(id, reason, fuzzTime)
		if perr != nil {
			assertDecisionError(t, perr)
			return
		}
		if !item.ValidID(item.Candidate, res.Record.Item) {
			t.Fatalf("Release accepted invalid candidate ID %q", res.Record.Item)
		}
		if len(res.Record.Reason) > item.MaxReason || !item.ValidText(res.Record.Reason, item.MaxReason) {
			t.Fatalf("Release accepted invalid reason (%d bytes)", len(res.Record.Reason))
		}
		if !res.Record.Sealed() {
			t.Fatalf("Release record is not sealed: %+v", res.Record)
		}
	})
}
