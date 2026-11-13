package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/clearedstack/countersign/internal/fault"
	"github.com/clearedstack/countersign/internal/queue"
)

func TestSaveThenLoadGivesTheSameBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Hold(queue.Item{ID: "cand-0001", Kind: queue.KindCandidate, State: queue.StateScored})
	if _, ferr := s.Decide("cand-0001", queue.OutcomeReject, "Too close to an existing tool."); ferr != nil {
		t.Fatal(ferr)
	}
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(path)
	again, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Save(path); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) != string(second) || len(first) == 0 {
		t.Fatalf("the store did not survive a round trip:\n%s\n%s", first, second)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".countersign-*")); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}

func TestAStoreThatDisagreesWithItselfIsRefused(t *testing.T) {
	item := `{"id": "cand-0001", "kind": "candidate", "state": "scored"}`
	dec := `{"seq": 1, "item_id": "cand-0001", "outcome": "reject", "from": "scored", "to": "rejected"}`
	for name, doc := range map[string]string{
		"duplicate id":            `{"version": 1, "items": [` + item + `,` + item + `], "decisions": []}`,
		"decision for no item":    `{"version": 1, "items": [], "decisions": [` + dec + `]}`,
		"two decisions, one item": `{"version": 1, "items": [` + item + `], "decisions": [` + dec + `,` + dec + `]}`,
		"unknown field":           `{"version": 1, "items": [], "decisions": [], "extra": true}`,
		"missing decisions":       `{"version": 1, "items": []}`,
		"trailing data":           `{"version": 1, "items": [], "decisions": []} {}`,
	} {
		path := filepath.Join(t.TempDir(), "store.json")
		if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := Load(path)
		var ferr *fault.Error
		if !errors.As(err, &ferr) || ferr.Code != fault.Parse {
			t.Errorf("%s: got %v, want E_PARSE", name, err)
		}
	}
}

func TestAnUnwritableStoreSaysNothingAboutThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-such-directory", "store.json")
	err := (&Store{Version: Version}).Save(path)
	if err != ErrIO {
		t.Fatalf("got %v, want ErrIO", err)
	}
}
