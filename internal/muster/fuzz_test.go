package muster_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/muster"
	"github.com/ClearedStack/countersign/internal/rule"
)

func assertLeaseError(t *testing.T, err *rule.Error) {
	t.Helper()
	info, ok := rule.Lookup(err.Rule)
	if !ok {
		t.Fatalf("unknown rule %q", err.Rule)
	}
	if info.Code != rule.Parse && info.Code != rule.NeedsSubtool {
		t.Fatalf("unexpected lease error code %q for rule %q", info.Code, err.Rule)
	}
	if err.Item != "" && !rule.SafeID(err.Item) {
		t.Fatalf("unsafe item ID %q in lease error", err.Item)
	}
	if len(err.Fingerprint()) != 16 {
		t.Fatalf("unexpected fingerprint %q", err.Fingerprint())
	}
}

func FuzzDecodeLease(f *testing.F) {
	seeds := []string{
		`{"lease":"PA-0001.4.1","kind":"item","id":"PA-0001","state":"scored","lane":"normal","attempt":1,"expires":"2026-01-05T10:02:00Z","found":{"source":"sam-gov","title":"Sample rate tool","address":"https://example.gov/1"},"results":{"assay":{"score":82,"recommendation":"build","split_flag":false,"gates":{"one_job":{"passed":true,"reason":"ok"}}}}}`,
		`{"lease":"PA-0002.4.1","kind":"item","id":"PA-0002","state":"screened_out","lane":"backfill","attempt":1,"expires":"2026-01-05T10:02:00Z","results":{"sextant":{"core_job":{"value":"Sample bid tool"}},"assay":{"gates":{"one_job":{"passed":false,"reason":"Line 1\r\nLine 2"}}}}}`,
		`{"lease":"EP-0001.1.1","kind":"proposal","id":"EP-0001","state":"pending_approval","lane":"urgent","attempt":1,"expires":"2026-01-05T10:02:00Z","proposal":{"target":"catalog/sample/desc","repository":"clearedstack.com","file":"data.json","edit_kind":"DATA","change":"Fix\r\ntypo","reason":"Spelling\rfix"}}`,
		`{"lease":"PA-0003.1.1","kind":"item","id":"PA-0003","state":"scored","lane":"normal","extra":true}`,
		`{"lease":"bad token","kind":"item","id":"PA-0004","state":"scored","lane":"normal"}`,
		`not-json`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		l, perr := muster.DecodeLease(data)
		if l.Token != "" && !muster.ValidToken(l.Token) {
			t.Fatalf("DecodeLease returned invalid token %q", l.Token)
		}
		if perr != nil {
			assertLeaseError(t, perr)
			return
		}
		if !muster.ValidToken(l.Token) {
			t.Fatalf("accepted lease has invalid token %q", l.Token)
		}

		it, ierr := l.Item()
		if ierr != nil {
			assertLeaseError(t, ierr)
			return
		}
		if cerr := item.Check(it); cerr != nil {
			t.Fatalf("Lease.Item produced item rejected by item.Check: %v", cerr)
		}
		if strings.Contains(it.Change, "\r") || strings.Contains(it.Reason, "\r") || strings.Contains(it.ScreenReason, "\r") {
			t.Fatalf("Lease.Item left CR characters in normalized text: %+v", it)
		}
		encoded, err := json.Marshal(it)
		if err != nil {
			t.Fatalf("accepted lease item failed to marshal: %v", err)
		}
		parsed, rerr := item.Parse(encoded)
		if rerr != nil || !reflect.DeepEqual(it, parsed) {
			t.Fatalf("round-trip item.Parse mismatch: err=%v\ngot:  %+v\nwant: %+v", rerr, parsed, it)
		}
	})
}
