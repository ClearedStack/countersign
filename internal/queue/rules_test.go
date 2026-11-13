package queue

import (
	"testing"

	"github.com/clearedstack/countersign/internal/fault"
)

// TestOnlyTheListedOutcomesAreAllowed tries every outcome against every kind
// and state. The expected table is written out again here on purpose, so a
// change to the rules has to be made twice to pass.
func TestOnlyTheListedOutcomesAreAllowed(t *testing.T) {
	want := map[[3]string]string{
		{"candidate", "scored", "approve"}:               "approved",
		{"candidate", "scored", "link"}:                  "linked",
		{"candidate", "scored", "merge"}:                 "merged",
		{"candidate", "scored", "park"}:                  "parked",
		{"candidate", "scored", "reject"}:                "rejected",
		{"candidate", "screened-out", "override"}:        "observed",
		{"edit_proposal", "pending_approval", "approve"}: "Approved",
		{"edit_proposal", "pending_approval", "decline"}: "declined",
	}
	kinds := []string{"candidate", "edit_proposal", "bundle"}
	states := []string{"scored", "screened-out", "pending_approval", "observed", "approved", "Approved", "declined"}
	outcomes := []string{"approve", "link", "merge", "park", "reject", "override", "decline"}
	for _, kind := range kinds {
		for _, state := range states {
			for _, outcome := range outcomes {
				d, ferr := Decide(Item{ID: "x-1", Kind: kind, State: state}, outcome, "A written reason.")
				to, ok := want[[3]string{kind, state, outcome}]
				switch {
				case ok && (ferr != nil || d.To != to || d.From != state):
					t.Errorf("%s %s %s: got %+v, %v; want state %s", kind, state, outcome, d, ferr, to)
				case !ok && (ferr == nil || ferr.Code != fault.Parse || ferr.Rule != "outcome-not-allowed"):
					t.Errorf("%s %s %s: got %+v, %v; want a refusal", kind, state, outcome, d, ferr)
				}
			}
		}
	}
	if len(Rules()) != len(want) {
		t.Errorf("the table has %d rows, the test expects %d", len(Rules()), len(want))
	}
}
