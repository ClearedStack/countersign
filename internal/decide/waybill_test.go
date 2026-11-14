package decide_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
)

// calculator is Waybill's own accepted example, copied from
// tools/spec-profiles/calculator/testdata/ok-capped-allowance/record.json in
// ClearedStack/waybill, branch consort/ready-to-deploy (commit 52c51a9).
// Two edits were made, the same two Waybill's worker test makes to it before
// it plays it as results.countersign: item_id and state are left out (the
// leased item supplies them) and the authority carries an official https url.
const calculator = "../../testdata/waybill/ok-capped-allowance.json"

func readCalculator(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(calculator)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// edit returns the calculator specification with one top-level key set, or
// removed when value is nil.
func edit(t *testing.T, key string, value any) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(readCalculator(t), &m); err != nil {
		t.Fatal(err)
	}
	if value == nil {
		delete(m, key)
	} else {
		m[key] = value
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// withLabel returns the calculator specification with a long summary (a field
// Waybill's record permits), written as it stands: encoding it with json would
// already turn each "<" into six bytes.
func withLabel(t *testing.T, label string) []byte {
	t.Helper()
	spec := string(readCalculator(t))
	out := strings.Replace(spec, `"title": "prorated`, `"summary": "`+label+`", "title": "prorated`, 1)
	if out == spec {
		t.Fatal("the calculator example has changed")
	}
	return []byte(out)
}

// padded returns the calculator specification, compact and otherwise valid,
// with its summary padded so that the file is exactly n bytes long.
func padded(t *testing.T, n int) []byte {
	t.Helper()
	base := edit(t, "summary", "")
	if n < len(base) {
		t.Fatalf("%d bytes is less than the specification itself", n)
	}
	return edit(t, "summary", strings.Repeat("a", n-len(base)))
}

func scored() item.Item {
	score := 82
	return item.Item{
		ID: "PA-0001", Kind: item.Candidate, State: item.Scored, Lane: item.Normal,
		Title: "Sample allowance calculator", Score: &score, Recommendation: item.Approved,
	}
}

func screenedOut() item.Item {
	return item.Item{
		ID: "PA-0002", Kind: item.Candidate, State: item.ScreenedOut, Lane: item.Normal,
		Title: "Sample suite", ScreenReason: "one_job: Too broad.",
	}
}

func proposal() item.Item {
	return item.Item{
		ID: "EP-0001", Kind: item.Proposal, State: item.PendingApproval, Lane: item.Urgent,
		Target: "catalog/sample/description", EditKind: "DATA", Change: "Fix spelling.",
	}
}

func TestApplyWaybillAllowedOrRefusedPerState(t *testing.T) {
	spec := readCalculator(t)
	tests := []struct {
		name    string
		it      item.Item
		d       decide.Decision
		allowed bool
	}{
		{name: "approve on a scored item", it: scored(),
			d: decide.Decision{Item: "PA-0001", Outcome: item.Approve, Waybill: spec}, allowed: true},
		{name: "override on a screened_out item", it: screenedOut(),
			d: decide.Decision{Item: "PA-0002", Outcome: item.Override, Reason: "Reshaped.", Waybill: spec}},
		{name: "link", it: scored(),
			d: decide.Decision{Item: "PA-0001", Outcome: item.Link, Tool: "t", Reason: "r", Waybill: spec}},
		{name: "merge", it: scored(),
			d: decide.Decision{Item: "PA-0001", Outcome: item.Merge, Parent: "p", Reason: "r", Waybill: spec}},
		{name: "park", it: scored(),
			d: decide.Decision{Item: "PA-0001", Outcome: item.Park, Blocker: "b", Reason: "r", Waybill: spec}},
		{name: "reject", it: scored(),
			d: decide.Decision{Item: "PA-0001", Outcome: item.Reject, Reason: "r", Waybill: spec}},
		{name: "approve on an edit proposal", it: proposal(),
			d: decide.Decision{Item: "EP-0001", Outcome: item.Approve, Waybill: spec}},
		{name: "decline on an edit proposal", it: proposal(),
			d: decide.Decision{Item: "EP-0001", Outcome: item.Decline, Waybill: spec}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := decide.Apply(tt.it, tt.d, fuzzTime)
			if tt.allowed {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				if !res.Record.Sealed() || len(res.Record.Waybill) == 0 {
					t.Fatalf("record carries no sealed waybill: %+v", res.Record)
				}
				return
			}
			if err == nil || err.Rule != rule.DecisionFieldUnexpected {
				t.Fatalf("err = %v, want %s", err, rule.DecisionFieldUnexpected)
			}
		})
	}
}

func TestApplyWithoutWaybillCarriesNone(t *testing.T) {
	res, err := decide.Apply(scored(), decide.Decision{Item: "PA-0001", Outcome: item.Approve}, fuzzTime)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res.Record)
	if bytes.Contains(b, []byte("waybill")) {
		t.Fatalf("record mentions a waybill: %s", b)
	}
}

func TestApplyWaybillIsCarriedUnchanged(t *testing.T) {
	spec := readCalculator(t)
	res, err := decide.Apply(scored(), decide.Decision{Item: "PA-0001", Outcome: item.Approve, Waybill: spec}, fuzzTime)
	if err != nil {
		t.Fatal(err)
	}
	b, jerr := json.Marshal(res.Record)
	if jerr != nil {
		t.Fatal(jerr)
	}
	var got struct {
		Outcome string          `json:"outcome"`
		Waybill json.RawMessage `json:"waybill"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	var want, have bytes.Buffer
	if err := json.Compact(&want, spec); err != nil {
		t.Fatal(err)
	}
	if err := json.Compact(&have, got.Waybill); err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "approve" || !bytes.Equal(want.Bytes(), have.Bytes()) {
		t.Fatalf("outcome %q, waybill\n%s\nwant\n%s", got.Outcome, have.Bytes(), want.Bytes())
	}
}

func TestParseWaybill(t *testing.T) {
	tests := []struct {
		name string
		file []byte
		ok   bool
	}{
		{name: "calculator example", file: readCalculator(t), ok: true},
		{name: "item id of the item", file: edit(t, "item_id", "PA-0001"), ok: true},
		{name: "state the item moves to", file: edit(t, "state", "approved"), ok: true},
		{name: "item id of another item", file: edit(t, "item_id", "PA-0002")},
		{name: "item id that is not text", file: edit(t, "item_id", 1)},
		{name: "state the item does not move to", file: edit(t, "state", "building")},
		{name: "empty", file: nil},
		{name: "not json", file: []byte(`{"title":`)},
		{name: "an array", file: []byte(`[]`)},
		{name: "text after the object", file: append(readCalculator(t), `{}`...)},
		{name: "duplicate top-level key", file: []byte(strings.Replace(string(readCalculator(t)), `"title"`, `"tool_type": "calculator", "title"`, 1))},
		{name: "duplicate nested key", file: []byte(strings.Replace(string(readCalculator(t)), `"decision": "build"`, `"decision": "build", "decision": "build"`, 1))},
		{name: "exactly the size limit", file: padded(t, item.MaxWaybill), ok: true},
		{name: "one byte over the size limit", file: padded(t, item.MaxWaybill+1)},
		{name: "unknown top-level field", file: edit(t, "price", 1)},
		{name: "unknown nested field", file: edit(t, "source", map[string]any{"product": "Sample Tool", "vendor": "Sample Maker", "url": "x"})},
		{name: "title missing", file: edit(t, "title", nil)},
		{name: "title empty", file: edit(t, "title", "")},
		{name: "title not text", file: edit(t, "title", 7)},
		{name: "tool type missing", file: edit(t, "tool_type", nil)},
		{name: "tool missing", file: edit(t, "tool", nil)},
		{name: "tool not an object", file: edit(t, "tool", []any{})},
		{name: "tool null", file: edit(t, "tool", json.RawMessage("null"))},
		{name: "authority missing", file: edit(t, "authority", nil)},
		{name: "authority empty", file: edit(t, "authority", []any{})},
		{name: "authority without a url", file: edit(t, "authority", []any{map[string]any{"id": "a", "source": "FAR", "citation": "c", "title": "t"}})},
		{name: "authority url not https", file: edit(t, "authority", []any{map[string]any{"id": "a", "source": "FAR", "citation": "c", "title": "t", "url": "http://www.acquisition.gov/far"}})},
		{name: "forbidden observation key", file: edit(t, "tool", map[string]any{"raw_html": "<p>x</p>"})},
		{name: "forbidden observation key nested deeply", file: edit(t, "tool", map[string]any{"a": []any{map[string]any{"screenshot": "s"}}})},
		{name: "vendor observations with free text", file: edit(t, "vendor_observations", map[string]any{"notes": "anything"})},
		{name: "vendor observations that are text", file: edit(t, "vendor_observations", "notes")},
		{name: "vendor observations that are empty", file: edit(t, "vendor_observations", map[string]any{})},
		{name: "vendor observations that are an empty list", file: edit(t, "vendor_observations", []any{})},
		{name: "vendor observations that are null", file: edit(t, "vendor_observations", json.RawMessage("null")), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decide.ParseWaybill(tt.file, "PA-0001", item.Approved)
			if tt.ok {
				if err != nil || len(got) == 0 {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			if err == nil || err.Rule != rule.DecisionMalformed || err.Item != "PA-0001" {
				t.Fatalf("got %q, %v; want %s", got, err, rule.DecisionMalformed)
			}
		})
	}
}

func TestApplyWaybillKeepsTheWholeBodyWithinMusterLimit(t *testing.T) {
	// A specification inside the file limit whose "<" signs grow sixfold when
	// the record is encoded must still be refused when the body would pass
	// 64 KiB.
	spec := withLabel(t, strings.Repeat("<", 20<<10))
	// ParseWaybill alone accepts it: the file is within its limit.
	if _, err := decide.ParseWaybill(spec, "PA-0001", item.Approved); err != nil {
		t.Fatalf("the file alone was refused: %v", err)
	}
	_, err := decide.Apply(scored(), decide.Decision{Item: "PA-0001", Outcome: item.Approve, Waybill: spec}, fuzzTime)
	if err == nil || err.Rule != rule.DecisionMalformed {
		t.Fatalf("err = %v, want %s", err, rule.DecisionMalformed)
	}

	// A large but fitting specification is accepted, and the whole complete
	// body Muster will see is within the limit.
	fit := withLabel(t, strings.Repeat("a", 40<<10))
	res, err := decide.Apply(scored(), decide.Decision{Item: "PA-0001", Outcome: item.Approve, Waybill: fit}, fuzzTime)
	if err != nil {
		t.Fatal(err)
	}
	result, jerr := json.Marshal(res.Record)
	if jerr != nil {
		t.Fatal(jerr)
	}
	body, jerr := json.Marshal(map[string]any{
		"contract": record.ContractVersion, "lease": "PA-123456789.123456.12", "state": res.Record.To, "result": json.RawMessage(result),
	})
	if jerr != nil {
		t.Fatal(jerr)
	}
	if len(body) > 64<<10 {
		t.Fatalf("complete body is %d bytes", len(body))
	}
}

// recordSize is the size of the record Apply makes for the specification,
// encoded as the worker sends it.
func recordSize(t *testing.T, spec []byte) (int, *rule.Error) {
	t.Helper()
	res, perr := decide.Apply(scored(), decide.Decision{Item: "PA-0001", Outcome: item.Approve, Waybill: spec}, fuzzTime)
	if perr != nil {
		return 0, perr
	}
	b, err := json.Marshal(res.Record)
	if err != nil {
		t.Fatal(err)
	}
	return len(b), nil
}

func TestApplyWaybillRecordLimitIsExact(t *testing.T) {
	// Each "<" is six bytes in the record and one in the file, so a
	// specification well within the file limit can reach the record limit
	// to the byte: k of them and m plain bytes add 6k+m to the record.
	empty, perr := recordSize(t, withLabel(t, ""))
	if perr != nil {
		t.Fatal(perr)
	}
	room := item.MaxResult - empty
	if room < 6 {
		t.Fatalf("no room for a label: %d", room)
	}
	label := func(extra int) []byte {
		n := room + extra
		return withLabel(t, strings.Repeat("<", n/6)+strings.Repeat("a", n%6))
	}

	got, perr := recordSize(t, label(0))
	if perr != nil || got != item.MaxResult {
		t.Fatalf("a record of exactly the limit: %d bytes, %v", got, perr)
	}
	if _, perr := recordSize(t, label(1)); perr == nil || perr.Rule != rule.DecisionMalformed {
		t.Fatalf("a record one byte over the limit: %v", perr)
	}
}

func FuzzParseWaybill(f *testing.F) {
	seed, err := os.ReadFile(calculator)
	if err != nil {
		f.Fatal(err)
	}
	for _, s := range [][]byte{
		seed,
		[]byte(`{}`),
		[]byte(`{"title":"t","tool_type":"calculator","tool":{},"authority":[{"id":"a","source":"FAR","citation":"c","title":"t","url":"https://www.acquisition.gov/far"}]}`),
		[]byte(`{"title":"t","title":"u"}`),
		[]byte(`{"vendor_observations":{"raw_html":"x"}}`),
		[]byte(`[]`),
		nil,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, file []byte) {
		got, perr := decide.ParseWaybill(file, "PA-0001", item.Approved)
		if perr != nil {
			if perr.Rule != rule.DecisionMalformed || perr.Item != "PA-0001" || perr.Code() != rule.Parse {
				t.Fatalf("unexpected refusal %+v", perr)
			}
			return
		}
		if len(file) > item.MaxWaybill {
			t.Fatalf("accepted %d bytes", len(file))
		}
		// What is accepted is one object that carries the parts Waybill
		// requires. Field names match as Waybill's own decoder matches them,
		// without regard to case.
		var req struct {
			Title     string            `json:"title"`
			ToolType  string            `json:"tool_type"`
			Tool      json.RawMessage   `json:"tool"`
			Authority []json.RawMessage `json:"authority"`
		}
		if err := json.Unmarshal(got, &req); err != nil {
			t.Fatalf("accepted something that is not a specification: %q", got)
		}
		if req.Title == "" || req.ToolType == "" || len(req.Tool) == 0 || req.Tool[0] != '{' || len(req.Authority) == 0 {
			t.Fatalf("accepted without a required part: %q", got)
		}
		again, perr := decide.ParseWaybill(got, "PA-0001", item.Approved)
		if perr != nil || !bytes.Equal(again, got) {
			t.Fatalf("not a fixed point: %q then %q, %v", got, again, perr)
		}
		// The record that carries it is sealed and survives the spool.
		res, perr := decide.Apply(scored(), decide.Decision{Item: "PA-0001", Outcome: item.Approve, Waybill: file}, fuzzTime)
		if perr != nil {
			if perr.Rule != rule.DecisionMalformed {
				t.Fatalf("unexpected refusal %+v", perr)
			}
			return
		}
		b, err := json.Marshal(res.Record)
		if err != nil {
			t.Fatal(err)
		}
		var back record.Record
		if err := json.Unmarshal(b, &back); err != nil || !back.Sealed() {
			t.Fatalf("record does not survive the spool: %v", err)
		}
	})
}
