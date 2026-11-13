package queue

import (
	"strings"

	"github.com/clearedstack/countersign/internal/fault"
)

// Outcomes a person may record.
const (
	OutcomeApprove  = "approve"
	OutcomeLink     = "link"
	OutcomeMerge    = "merge"
	OutcomePark     = "park"
	OutcomeReject   = "reject"
	OutcomeOverride = "override"
	OutcomeDecline  = "decline"
)

// Rule is one allowed decision: for an item of this kind in this state, this
// outcome moves it to that state.
type Rule struct {
	Kind        string
	From        string
	Outcome     string
	To          string
	NeedsReason bool
}

// rules is the whole table. An outcome that is not listed here for the
// item's kind and state is refused.
var rules = []Rule{
	{KindCandidate, StateScored, OutcomeApprove, "approved", false},
	{KindCandidate, StateScored, OutcomeLink, "linked", false},
	{KindCandidate, StateScored, OutcomeMerge, "merged", false},
	{KindCandidate, StateScored, OutcomePark, "parked", false},
	{KindCandidate, StateScored, OutcomeReject, "rejected", false},
	{KindCandidate, StateScreenedOut, OutcomeOverride, "observed", true},
	{KindEditProposal, StatePendingApproval, OutcomeApprove, "Approved", false},
	{KindEditProposal, StatePendingApproval, OutcomeDecline, "declined", false},
}

// Rules returns a copy of the table of allowed decisions, in a fixed order.
func Rules() []Rule {
	return append([]Rule(nil), rules...)
}

// Allowed lists the outcomes a person may record for an item, in table order.
// A paused item has none.
func Allowed(it Item) []string {
	if it.Paused != "" {
		return nil
	}
	var out []string
	for _, r := range rules {
		if r.Kind == it.Kind && r.From == it.State {
			out = append(out, r.Outcome)
		}
	}
	return out
}

// Decision is one recorded determination.
type Decision struct {
	Seq     int    `json:"seq"`
	ItemID  string `json:"item_id"`
	Outcome string `json:"outcome"`
	From    string `json:"from"`
	To      string `json:"to"`
	Reason  string `json:"reason,omitempty"`
}

// Decide checks a person's outcome for an item against the table. It returns
// the decision to record (without a sequence number), or the refusal.
func Decide(it Item, outcome, reason string) (Decision, *fault.Error) {
	known := false
	for _, r := range rules {
		if r.Outcome == outcome {
			known = true
			break
		}
	}
	if !known {
		return Decision{}, fault.New(fault.Parse, "outcome-unknown", "the outcome is not one Countersign records")
	}
	if it.Paused != "" {
		return Decision{}, fault.New(fault.NeedsSubtool, "item-paused", "the item is paused because no rule covers it")
	}
	reason = strings.TrimSpace(reason)
	for _, r := range rules {
		if r.Kind != it.Kind || r.From != it.State || r.Outcome != outcome {
			continue
		}
		if r.NeedsReason && reason == "" {
			return Decision{}, fault.New(fault.Parse, "override-reason-missing", "an override needs a written reason")
		}
		return Decision{ItemID: it.ID, Outcome: outcome, From: r.From, To: r.To, Reason: reason}, nil
	}
	return Decision{}, fault.New(fault.Parse, "outcome-not-allowed", "the outcome is not allowed for this item in its current state")
}
