package record_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/record"
)

func complete(waybill string) record.Record {
	return record.Record{
		Call: record.Complete, Item: "PA-0001", At: "2026-01-05T10:00:00Z",
		Kind: item.Candidate, Outcome: item.Approve, From: item.Scored, To: item.Approved,
		Waybill: json.RawMessage(waybill),
	}.Seal()
}

func TestRecordWaybill(t *testing.T) {
	tests := []struct {
		name    string
		waybill string
	}{
		{name: "none"},
		{name: "plain", waybill: `{"title":"a"}`},
		{name: "html characters", waybill: `{"title":"a < b & c > d"}`},
		{name: "line separators", waybill: "{\"title\":\"a b\"}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := complete(tt.waybill)
			if !rec.Sealed() {
				t.Fatal("not sealed")
			}
			b, err := json.Marshal(rec)
			if err != nil {
				t.Fatal(err)
			}
			if tt.waybill == "" {
				if strings.Contains(string(b), "waybill") {
					t.Fatalf("an empty waybill is written: %s", b)
				}
			}
			// The spool writes the record and reads it back; the worker
			// writes it again for Muster. Nothing may change on the way.
			var back record.Record
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			again, err := json.Marshal(back)
			if err != nil || string(again) != string(b) || !back.Sealed() {
				t.Fatalf("round trip changed the record:\n%s\n%s\nsealed=%v err=%v", b, again, back.Sealed(), err)
			}
		})
	}
}

func TestSealCoversWaybill(t *testing.T) {
	a, b := complete(`{"title":"a"}`), complete(`{"title":"b"}`)
	if a.ID == b.ID {
		t.Fatal("two different waybills give one identifier")
	}
	a.Waybill = b.Waybill
	if a.Sealed() {
		t.Fatal("a changed waybill still reads as sealed")
	}
}
