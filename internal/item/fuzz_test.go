package item_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
)

func assertPublicIntakeError(t *testing.T, err *rule.Error) {
	t.Helper()
	info, ok := rule.Lookup(err.Rule)
	if !ok {
		t.Fatalf("unknown rule %q", err.Rule)
	}
	if info.Code != rule.Parse && info.Code != rule.NeedsSubtool {
		t.Fatalf("unexpected intake code %q for rule %q", info.Code, err.Rule)
	}
	if err.Item != "" && !rule.SafeID(err.Item) {
		t.Fatalf("unsafe item ID %q escaped in error", err.Item)
	}
	if len(err.Fingerprint()) != 16 {
		t.Fatalf("unexpected fingerprint %q", err.Fingerprint())
	}
}

func FuzzItemParse(f *testing.F) {
	seeds := []string{
		`{"id":"PA-0001","kind":"item","state":"scored","lane":"normal","title":"Sample rate check","score":82,"recommendation":"approved"}`,
		`{"id":"PA-0002","kind":"item","state":"screened_out","lane":"backfill","title":"Sample bid suite","screen_reason":"one_job: Three jobs."}`,
		`{"id":"EP-0001","kind":"proposal","state":"pending_approval","lane":"urgent","target":"catalog/sample/desc","repository":"clearedstack.com","file":"data.json","edit_kind":"DATA","change":"Fix typo.","reason":"Typo"}`,
		`{"id":"PA-0003","kind":"bundle","state":"scored","lane":"normal"}`,
		`{"id":"../bad","kind":"item","state":"scored","lane":"normal"}`,
		`not-json`,
		`{}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		it, perr := item.Parse(data)
		if perr != nil {
			assertPublicIntakeError(t, perr)
			return
		}
		if cerr := item.Check(it); cerr != nil {
			t.Fatalf("Parse accepted item that Check rejected: %v", cerr)
		}
		if len(item.Outcomes(it.Kind, it.State)) == 0 {
			t.Fatalf("accepted item (%s, %s) has zero allowed outcomes", it.Kind, it.State)
		}
		encoded, err := json.Marshal(it)
		if err != nil {
			t.Fatalf("accepted item failed to marshal: %v", err)
		}
		roundTrip, rerr := item.Parse(encoded)
		if rerr != nil {
			t.Fatalf("round-trip Parse failed: %v\nencoded: %s", rerr, encoded)
		}
		if !reflect.DeepEqual(it, roundTrip) {
			t.Fatalf("round-trip mismatch:\ngot:  %+v\nwant: %+v", roundTrip, it)
		}
	})
}
