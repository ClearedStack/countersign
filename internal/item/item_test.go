package item

import "testing"

func TestMovesAreExactlyTheSpecifiedOnes(t *testing.T) {
	type key struct {
		kind    Kind
		from    State
		outcome Outcome
	}
	want := map[key]State{
		{Candidate, Scored, Approve}:         Approved,
		{Candidate, Scored, Link}:            Linked,
		{Candidate, Scored, Merge}:           Merged,
		{Candidate, Scored, Park}:            Parked,
		{Candidate, Scored, Reject}:          Rejected,
		{Candidate, ScreenedOut, Override}:   Observed,
		{Proposal, PendingApproval, Approve}: Approved,
		{Proposal, PendingApproval, Decline}: Declined,
	}
	// A held item may be released or kept in any of the fourteen item
	// states, and stays in that state either way.
	fourteen := []State{Discovered, Recorded, Observed, ScreenedOut, Scored, Approved, Building, Live, Parked, Linked, Merged, Rejected, Retired, NeedsSubtool}
	if len(fourteen) != 14 {
		t.Fatal("there are fourteen item states")
	}
	for _, s := range fourteen {
		want[key{Release, s, LetGo}] = s
		want[key{Release, s, Keep}] = s
	}
	kinds := []Kind{Candidate, Proposal, Release, "bundle"}
	states := append([]State{PendingApproval, Declined, "galloping"}, fourteen...)
	outcomes := []Outcome{Approve, Link, Merge, Park, Reject, Override, Decline, LetGo, Keep, "escalate"}
	n := 0
	for _, k := range kinds {
		for _, s := range states {
			for _, o := range outcomes {
				to, ok := Next(k, s, o)
				wantTo, wantOK := want[key{k, s, o}]
				if ok != wantOK || to != wantTo {
					t.Errorf("Next(%s, %s, %s) = %q, %v; want %q, %v", k, s, o, to, ok, wantTo, wantOK)
				}
				if ok {
					n++
				}
			}
		}
	}
	if n != len(want) {
		t.Errorf("%d moves allowed, want %d", n, len(want))
	}
}

func TestOutcomesOrder(t *testing.T) {
	got := Outcomes(Candidate, Scored)
	want := []Outcome{Approve, Link, Merge, Park, Reject}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if len(Outcomes(Candidate, Parked)) != 0 {
		t.Error("a parked candidate has outcomes")
	}
}

func TestText(t *testing.T) {
	if !ValidText("line one\n\tline two", 100) || ValidLine("line one\nline two", 100) {
		t.Error("new lines: allowed in text, not in a line")
	}
	for _, s := range []string{"a\x00b", "a\x1bb", "a\x7fb", "a\u009bb", "a\rb", "\xff"} {
		if ValidText(s, 100) {
			t.Errorf("ValidText(%q) = true", s)
		}
	}
	if ValidText("abcd", 3) || !ValidText("abc", 3) {
		t.Error("length limit is in bytes, inclusive")
	}
	if !Blank(" \n\t ") || Blank(" x ") {
		t.Error("Blank")
	}
}
