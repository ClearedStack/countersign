package muster

import (
	"context"
	"errors"
	"testing"

	"github.com/clearedstack/countersign/internal/fault"
)

func TestMemoryHandsOutEachItemOnce(t *testing.T) {
	m, err := FromJSON([]byte(`{"items": [{"id": "cand-0001"}, {"id": "cand-0002"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var client Client = m
	first, err := client.Lease(context.Background())
	if err != nil || len(first) != 2 || string(first[0]) != `{"id": "cand-0001"}` {
		t.Fatalf("first lease: %q, %v", first, err)
	}
	second, err := client.Lease(context.Background())
	if err != nil || len(second) != 0 {
		t.Fatalf("second lease: %q, %v", second, err)
	}
}

func TestLeaseAfterDeadlineIsATimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewMemory(nil).Lease(ctx)
	var ferr *fault.Error
	if !errors.As(err, &ferr) || ferr.Code != fault.Timeout {
		t.Fatalf("got %v, want E_TIMEOUT", err)
	}
}

func TestUnreadableLeaseIsAParseError(t *testing.T) {
	for _, doc := range []string{``, `[]`, `{}`, `{"items": 3}`, `{"items": null}`} {
		_, err := FromJSON([]byte(doc))
		var ferr *fault.Error
		if !errors.As(err, &ferr) || ferr.Code != fault.Parse {
			t.Errorf("%q: got %v, want E_PARSE", doc, err)
		}
	}
}
