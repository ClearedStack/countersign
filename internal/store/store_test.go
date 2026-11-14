package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
)

var at = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

func scored(id string) []byte {
	return []byte(fmt.Sprintf(`{"id":%q,"kind":"item","state":"scored","lane":"normal","title":"A made-up tool","score":82,"recommendation":"approved"}`, id))
}

func open(t *testing.T, root string) *Store {
	t.Helper()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func add(t *testing.T, s *Store, id string) {
	t.Helper()
	it, perr := item.Parse(scored(id))
	if perr != nil {
		t.Fatalf("item %s: %v", id, perr)
	}
	if _, err := s.Accept(id+".1.1", it); err != nil {
		t.Fatalf("accept %s: %v", id, err)
	}
}

func ruleOf(err error) rule.ID {
	var e *rule.Error
	if errors.As(err, &e) {
		return e.Rule
	}
	return ""
}

func TestDecisionSurvivesAReopen(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	add(t, s, "PA-0001")
	add(t, s, "PA-0002")
	if _, err := s.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Approve}, at); err != nil {
		t.Fatal(err)
	}

	s = open(t, root) // as after a restart
	out, err := s.Outbox()
	if err != nil || len(out) != 1 || out[0].Record.Item != "PA-0001" || out[0].Status != Waiting || out[0].Lease != "PA-0001.1.1" {
		t.Fatalf("outbox after reopen: %+v %v", out, err)
	}
	pending, _ := s.Pending()
	if len(pending) != 1 || pending[0].Item.ID != "PA-0002" {
		t.Fatalf("pending after reopen: %+v", pending)
	}
}

func TestRefusedDecisionChangesNothing(t *testing.T) {
	s := open(t, t.TempDir())
	it, perr := item.Parse([]byte(`{"id":"PA-0002","kind":"item","state":"screened_out","lane":"normal","title":"A made-up tool","screen_reason":"Fails a gate."}`))
	if perr != nil {
		t.Fatal(perr)
	}
	if _, err := s.Accept("PA-0002.1.1", it); err != nil {
		t.Fatal(err)
	}
	_, err := s.Decide(decide.Decision{Item: "PA-0002", Outcome: item.Override}, at)
	if ruleOf(err) != rule.DecisionOverrideReasonMissing {
		t.Fatalf("got %v", err)
	}
	if pending, _ := s.Pending(); len(pending) != 1 {
		t.Error("the item left the inbox")
	}
	if out, _ := s.Outbox(); len(out) != 0 {
		t.Error("a refused decision was spooled")
	}
}

func TestOnlyOneDecisionWins(t *testing.T) {
	root := t.TempDir()
	add(t, open(t, root), "PA-0001")
	const n = 16
	var wg sync.WaitGroup
	results := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine opens the directory for itself, as separate
			// processes would.
			s, err := Open(root)
			if err != nil {
				results[i] = err
				return
			}
			_, results[i] = s.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Approve}, at)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case ruleOf(err) != rule.DecisionItemUnknown:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if won != 1 {
		t.Errorf("%d decisions were recorded for one item", won)
	}
	if out, _ := open(t, root).Outbox(); len(out) != 1 {
		t.Errorf("outbox holds %d records", len(out))
	}
}

func TestStopBetweenSpoolAndInbox(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	add(t, s, "PA-0001")
	saved, err := os.ReadFile(filepath.Join(root, "pending", "PA-0001.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Approve}, at); err != nil {
		t.Fatal(err)
	}
	// Put the item back, as if the process had stopped after spooling the
	// decision and before taking the item out of the inbox.
	if err := os.WriteFile(filepath.Join(root, "pending", "PA-0001.json"), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	s = open(t, root)
	_, err = s.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Reject, Reason: "Second thoughts."}, at)
	if ruleOf(err) != rule.DecisionItemUnknown {
		t.Fatalf("a second decision was accepted: %v", err)
	}
	if out, _ := s.Outbox(); len(out) != 1 || out[0].Record.Outcome != item.Approve {
		t.Errorf("outbox: %+v", out)
	}
	if pending, _ := s.Pending(); len(pending) != 0 {
		t.Error("the decided item is still in the inbox")
	}
}

func TestAckRefuseAndPrune(t *testing.T) {
	s := open(t, t.TempDir())
	total := KeepDone + 5
	for i := 1; i <= total; i++ {
		id := fmt.Sprintf("PA-%04d", i)
		add(t, s, id)
		if _, err := s.Decide(decide.Decision{Item: id, Outcome: item.Approve}, at); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := s.Outbox()
	if len(out) != total {
		t.Fatalf("outbox holds %d", len(out))
	}
	for i, e := range out {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d: the spool is out of order", i, e.Seq)
		}
		var err error
		if i == 0 {
			err = s.Refuse(e.Seq, "abc")
		} else {
			err = s.Ack(e.Seq)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Ack(1); err != nil {
		t.Errorf("finishing twice: %v", err)
	}
	log, _ := s.Log()
	if len(log) != KeepDone {
		t.Fatalf("log holds %d, want %d", len(log), KeepDone)
	}
	if log[len(log)-1].Seq != uint64(total) || log[len(log)-1].Status != Acknowledged {
		t.Errorf("newest log entry: %+v", log[len(log)-1])
	}
	// Numbers keep rising after pruning.
	add(t, s, "PA-9999")
	if _, err := s.Decide(decide.Decision{Item: "PA-9999", Outcome: item.Approve}, at); err != nil {
		t.Fatal(err)
	}
	out, _ = s.Outbox()
	if len(out) != 1 || out[0].Seq != uint64(total+1) {
		t.Errorf("next seq: %+v", out)
	}
}

func TestLeasedAgainKeepsItsPlace(t *testing.T) {
	s := open(t, t.TempDir())
	add(t, s, "PA-0001")
	add(t, s, "PA-0002")
	add(t, s, "PA-0001")
	pending, _ := s.Pending()
	if len(pending) != 2 || pending[0].Item.ID != "PA-0001" || pending[1].Item.ID != "PA-0002" {
		t.Errorf("pending: %+v", pending)
	}
}

func TestOpenClearsHalfWrittenFiles(t *testing.T) {
	root := t.TempDir()
	open(t, root)
	stray := filepath.Join(root, "outbox", ".tmp-123")
	if err := os.WriteFile(stray, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := open(t, root)
	if _, err := os.Stat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Error("a half-written file was kept")
	}
	if out, err := s.Outbox(); err != nil || len(out) != 0 {
		t.Errorf("outbox: %v %v", out, err)
	}
}

func TestDiskErrorsNeverNameAPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secret-mount-name")
	if err := os.WriteFile(root, nil, 0o600); err != nil { // a file, not a directory
		t.Fatal(err)
	}
	_, err := Open(root)
	if ruleOf(err) != rule.StoreUnavailable {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), "secret-mount-name") || strings.Contains(err.Error(), string(filepath.Separator)) {
		t.Errorf("the error names a path: %v", err)
	}
}

func TestGetRejectsUnsafeIdentifiers(t *testing.T) {
	root := t.TempDir()
	s := open(t, root)
	if err := os.WriteFile(filepath.Join(root, "lock.json"), []byte(`{"arrived":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"../lock", "..", "", "a/b"} {
		if _, err := s.Get(id); ruleOf(err) != rule.DecisionItemUnknown {
			t.Errorf("Get(%q) = %v", id, err)
		}
	}
}

func TestReportSaysTheSameThingOnce(t *testing.T) {
	s := open(t, t.TempDir())
	add(t, s, "PA-0001")
	perr := rule.Fail(rule.IntakeKindUnfamiliar, "PA-0001")

	rec, err := s.Report("PA-0001.2.2", perr, at)
	if err != nil || rec == nil || rec.Call != "needs_subtool" || rec.Code != "E_NEEDS_SUBTOOL" {
		t.Fatalf("first report: %+v %v", rec, err)
	}
	if pending, _ := s.Pending(); len(pending) != 0 {
		t.Error("the reported item stayed in the inbox")
	}
	// Waiting: not said again.
	if rec, err = s.Report("PA-0001.3", perr, at); err != nil || rec != nil {
		t.Errorf("second report while the first waits: %+v %v", rec, err)
	}
	// Refused by Muster: still not said again.
	if err := s.Refuse(1, "abc"); err != nil {
		t.Fatal(err)
	}
	if rec, err = s.Report("PA-0001.4", perr, at); err != nil || rec != nil {
		t.Errorf("report after a refusal: %+v %v", rec, err)
	}
	// A different rule about the same item is a different report.
	other := rule.Fail(rule.IntakeFieldMissing, "PA-0001")
	if rec, err = s.Report("PA-0001.5", other, at); err != nil || rec == nil || rec.Call != "fail" || rec.Code != "E_PARSE" {
		t.Errorf("a different report: %+v %v", rec, err)
	}
	out, _ := s.Outbox()
	if len(out) != 1 || out[0].Lease != "PA-0001.5" {
		t.Errorf("outbox: %+v", out)
	}
}

func TestALostLeaseKeepsTheDecisionForTheNextLease(t *testing.T) {
	s := open(t, t.TempDir())
	add(t, s, "PA-0001")
	add(t, s, "PA-0002")
	for _, id := range []string{"PA-0001", "PA-0002"} {
		if _, err := s.Decide(decide.Decision{Item: id, Outcome: item.Approve}, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Unlease(1); err != nil {
		t.Fatal(err)
	}
	out, _ := s.Outbox()
	if len(out) != 2 || out[0].Status != Unleased || out[1].Status != Waiting {
		t.Fatalf("outbox: %+v", out)
	}

	// Muster offers the item again, unchanged: the decision takes the new
	// lease, and the owner is not asked again.
	it, _ := item.Parse(scored("PA-0001"))
	arrival, err := s.Accept("PA-0001.5.2", it)
	if err != nil || !arrival.Resent || arrival.Stale {
		t.Fatalf("arrival: %+v %v", arrival, err)
	}
	if pending, _ := s.Pending(); len(pending) != 0 {
		t.Errorf("the item was put before the owner again: %+v", pending)
	}
	out, _ = s.Outbox()
	if len(out) != 2 || out[0].Seq != 1 || out[0].Status != Waiting || out[0].Lease != "PA-0001.5.2" || out[0].Record.Outcome != item.Approve {
		t.Fatalf("outbox after the new lease: %+v", out)
	}
}

func TestAnItemThatComesBackChangedSetsTheDecisionAside(t *testing.T) {
	s := open(t, t.TempDir())
	add(t, s, "PA-0001")
	if _, err := s.Decide(decide.Decision{Item: "PA-0001", Outcome: item.Approve}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.Unlease(1); err != nil {
		t.Fatal(err)
	}
	changed, perr := item.Parse([]byte(`{"id":"PA-0001","kind":"item","state":"screened_out","lane":"normal","title":"A made-up tool","screen_reason":"Fails a gate."}`))
	if perr != nil {
		t.Fatal(perr)
	}
	arrival, err := s.Accept("PA-0001.6.1", changed)
	if err != nil || arrival.Resent || !arrival.Stale {
		t.Fatalf("arrival: %+v %v", arrival, err)
	}
	if out, _ := s.Outbox(); len(out) != 0 {
		t.Errorf("the stale decision is still to be sent: %+v", out)
	}
	pending, _ := s.Pending()
	if len(pending) != 1 || pending[0].Item.State != item.ScreenedOut || pending[0].Lease != "PA-0001.6.1" {
		t.Errorf("inbox: %+v", pending)
	}
	log, _ := s.Log()
	if len(log) != 1 || log[0].Status != Refused || log[0].Fingerprint != rule.Fail(rule.SpoolStale, "PA-0001").Fingerprint() {
		t.Errorf("log: %+v", log)
	}
}

func TestReleaseIsSpooledWithoutALease(t *testing.T) {
	s := open(t, t.TempDir())
	res, err := s.Release("PA-0007", "  The source was corrected.  ", at)
	if err != nil || res.Rule != rule.DecisionRelease || res.Record.Call != "release" || res.Record.Item != "PA-0007" || res.Record.Reason != "The source was corrected." {
		t.Fatalf("release: %+v %v", res, err)
	}
	out, _ := s.Outbox()
	if len(out) != 1 || out[0].Lease != "" || out[0].Status != Waiting {
		t.Fatalf("outbox: %+v", out)
	}
	for _, id := range []string{"EP-0001", "PA-1", "../x", ""} {
		if _, err := s.Release(id, "", at); ruleOf(err) != rule.DecisionMalformed {
			t.Errorf("Release(%q) = %v", id, err)
		}
	}
	if _, err := s.Release("PA-0008", "bad \x1b[31m text", at); ruleOf(err) != rule.DecisionTextInvalid {
		t.Errorf("reason with an escape: %v", err)
	}
	if out, _ := s.Outbox(); len(out) != 1 {
		t.Errorf("a refused release was spooled")
	}
}
