// Package decide applies the owner's decision to one item. It never chooses
// an outcome. It only says whether the decision the owner made is allowed
// and complete, and writes it down.
//
// Everything here is a pure function of its input.
package decide

import (
	"strings"
	"time"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// Decision is what the owner submits.
type Decision struct {
	Item    string       `json:"item"`
	Outcome item.Outcome `json:"outcome"`
	Reason  string       `json:"reason,omitempty"`
	Tool    string       `json:"tool,omitempty"`    // link: the existing tool
	Parent  string       `json:"parent,omitempty"`  // merge: the parent tool
	Blocker string       `json:"blocker,omitempty"` // park: what is in the way
}

// Result is an accepted decision: the rule that accepted it and the record
// to hand to Muster.
type Result struct {
	Rule   rule.ID       `json:"rule"`
	Record record.Record `json:"record"`
}

// Parse reads a decision from JSON.
func Parse(payload []byte) (Decision, *rule.Error) {
	var d Decision
	if len(payload) > item.MaxPayload {
		return Decision{}, rule.Fail(rule.DecisionMalformed, "")
	}
	if err := strictjson.Decode(payload, &d); err != nil {
		return Decision{}, rule.Fail(rule.DecisionMalformed, "")
	}
	if !rule.SafeID(d.Item) || d.Outcome == "" {
		return Decision{}, rule.Fail(rule.DecisionMalformed, d.Item)
	}
	return d, nil
}

var accepted = map[item.Kind]map[item.Outcome]rule.ID{
	item.Candidate: {
		item.Approve:  rule.DecisionApprove,
		item.Link:     rule.DecisionLink,
		item.Merge:    rule.DecisionMerge,
		item.Park:     rule.DecisionPark,
		item.Reject:   rule.DecisionReject,
		item.Override: rule.DecisionOverride,
	},
	item.Proposal: {
		item.Approve: rule.DecisionProposalApprove,
		item.Decline: rule.DecisionProposalDecline,
	},
	item.Release: {
		item.LetGo: rule.DecisionRelease,
		item.Keep:  rule.DecisionKeep,
	},
}

// Apply checks a decision against the item it is for. The checks run in a
// fixed order and the first that fails is the one reported:
//
//  1. the outcome is one Countersign knows;
//  2. the outcome is allowed for an item of this kind in this state;
//  3. every text field is safe text within its limit;
//  4. no field is present that the outcome does not take;
//  5. the field the outcome needs is there: the tool for a link, the parent
//     for a merge, the blocker for a park, the reason for a rejection;
//  6. an override has a written reason. An override is reopening a
//     screened_out item, or choosing against the recommendation.
//
// Releasing or keeping a held item moves it nowhere: it keeps the state it
// is held in. A reason may be given and is not required.
func Apply(it item.Item, d Decision, at time.Time) (Result, *rule.Error) {
	fail := func(id rule.ID) (Result, *rule.Error) { return Result{}, rule.Fail(id, it.ID) }

	if !item.KnownOutcome(d.Outcome) {
		return fail(rule.DecisionOutcomeUnknown)
	}
	to, ok := item.Next(it.Kind, it.State, d.Outcome)
	if !ok {
		return fail(rule.DecisionOutcomeNotAllowed)
	}
	if !item.ValidText(d.Reason, item.MaxText) || !item.ValidText(d.Blocker, item.MaxText) ||
		!item.ValidLine(d.Tool, item.MaxLine) || !item.ValidLine(d.Parent, item.MaxLine) {
		return fail(rule.DecisionTextInvalid)
	}
	reason := strings.TrimSpace(d.Reason)
	tool := strings.TrimSpace(d.Tool)
	parent := strings.TrimSpace(d.Parent)
	blocker := strings.TrimSpace(d.Blocker)

	if (d.Tool != "" && d.Outcome != item.Link) ||
		(d.Parent != "" && d.Outcome != item.Merge) ||
		(d.Blocker != "" && d.Outcome != item.Park) {
		return fail(rule.DecisionFieldUnexpected)
	}
	switch {
	case d.Outcome == item.Link && tool == "":
		return fail(rule.DecisionLinkToolMissing)
	case d.Outcome == item.Merge && parent == "":
		return fail(rule.DecisionMergeParentMissing)
	case d.Outcome == item.Park && blocker == "":
		return fail(rule.DecisionParkBlockerMissing)
	case d.Outcome == item.Reject && reason == "":
		return fail(rule.DecisionRejectReasonMissing)
	}

	override := it.Kind == item.Candidate &&
		(it.State == item.ScreenedOut || (it.State == item.Scored && to != it.Recommendation))
	if override && reason == "" {
		return fail(rule.DecisionOverrideReasonMissing)
	}

	lane := it.Lane
	if it.Kind == item.Proposal && d.Outcome == item.Approve {
		// An edit the owner has approved goes on the urgent lane.
		lane = item.Urgent
	}

	rec := record.Record{
		Call:           record.Complete,
		Item:           it.ID,
		At:             record.Stamp(at),
		Kind:           it.Kind,
		Outcome:        d.Outcome,
		From:           it.State,
		To:             to,
		Lane:           lane,
		Override:       override,
		Score:          it.Score,
		Recommendation: it.Recommendation,
		Reason:         reason,
		Tool:           tool,
		Parent:         parent,
		Blocker:        blocker,
	}.Seal()
	return Result{Rule: accepted[it.Kind][d.Outcome], Record: rec}, nil
}
