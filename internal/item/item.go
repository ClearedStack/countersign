// Package item describes what Countersign holds for the owner: scored and
// screened-out candidates, and pending edit proposals. It checks an item
// arriving from Muster and holds the table of moves the owner may make.
//
// Everything here is a pure function of its input.
package item

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// Kind is what sort of item this is.
type Kind string

const (
	Candidate Kind = "candidate"
	Proposal  Kind = "proposal"
)

// State is an item's state. The candidate states are the ones the platform's
// item lifecycle names. Declined is provisional: the specification says a
// declined proposal is logged and does not give the state a name.
type State string

const (
	Scored          State = "scored"
	ScreenedOut     State = "screened-out"
	Observed        State = "observed"
	Approved        State = "approved"
	Linked          State = "linked"
	Merged          State = "merged"
	Parked          State = "parked"
	Rejected        State = "rejected"
	NeedsSubtool    State = "needs_subtool"
	PendingApproval State = "pending_approval"
	Declined        State = "declined"
)

// Lane is a priority lane. Urgent is emptied first, then normal, then backfill.
type Lane string

const (
	Urgent   Lane = "urgent"
	Normal   Lane = "normal"
	Backfill Lane = "backfill"
)

// LaneRank orders lanes: lower is taken first.
func LaneRank(l Lane) int {
	switch l {
	case Urgent:
		return 0
	case Normal:
		return 1
	case Backfill:
		return 2
	}
	return 3
}

// Outcome is a decision the owner can make.
type Outcome string

const (
	Approve  Outcome = "approve"
	Link     Outcome = "link"
	Merge    Outcome = "merge"
	Park     Outcome = "park"
	Reject   Outcome = "reject"
	Override Outcome = "override"
	Decline  Outcome = "decline"
)

type move struct {
	outcome Outcome
	to      State
}

type place struct {
	kind  Kind
	state State
}

// moves is the whole table of allowed decisions. Nothing else is allowed.
var moves = map[place][]move{
	{Candidate, Scored}: {
		{Approve, Approved},
		{Link, Linked},
		{Merge, Merged},
		{Park, Parked},
		{Reject, Rejected},
	},
	{Candidate, ScreenedOut}: {
		{Override, Observed},
	},
	{Proposal, PendingApproval}: {
		{Approve, Approved},
		{Decline, Declined},
	},
}

// KnownOutcome reports whether o is an outcome at all.
func KnownOutcome(o Outcome) bool {
	switch o {
	case Approve, Link, Merge, Park, Reject, Override, Decline:
		return true
	}
	return false
}

// Next returns the state an outcome moves an item to, if that is allowed.
func Next(kind Kind, from State, o Outcome) (State, bool) {
	for _, m := range moves[place{kind, from}] {
		if m.outcome == o {
			return m.to, true
		}
	}
	return "", false
}

// Outcomes lists the decisions allowed for an item, in a fixed order.
func Outcomes(kind Kind, state State) []Outcome {
	ms := moves[place{kind, state}]
	out := make([]Outcome, len(ms))
	for i, m := range ms {
		out[i] = m.outcome
	}
	return out
}

// Limits. A proposal's 4 KB is read as 4096 bytes of its body.
const (
	MaxPayload  = 32 << 10 // one item or decision, as received
	MaxLine     = 256      // a title, a target, a tool, a parent
	MaxText     = 4096     // a reason, a blocker
	MaxProposal = 4096     // the body of an edit proposal
	MinScore    = 20
	MaxScore    = 100
)

// Item is one thing waiting for the owner.
type Item struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	State State  `json:"state"`
	Lane  Lane   `json:"lane"`

	// Candidates.
	Title          string `json:"title,omitempty"`
	Score          *int   `json:"score,omitempty"`
	Recommendation State  `json:"recommendation,omitempty"`
	ScreenReason   string `json:"screen_reason,omitempty"`

	// Edit proposals.
	Type   string `json:"type,omitempty"`
	Target string `json:"target,omitempty"`
	Body   string `json:"body,omitempty"`
}

// Blank reports whether s holds nothing but white space.
func Blank(s string) bool { return strings.TrimSpace(s) == "" }

// ValidLine reports whether s is safe single-line text of at most max bytes.
func ValidLine(s string, max int) bool { return validText(s, max, false) }

// ValidText reports whether s is safe text of at most max bytes. New lines
// and tabs are allowed. Other control characters are not.
func ValidText(s string, max int) bool { return validText(s, max, true) }

func validText(s string, max int, multiline bool) bool {
	if len(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if multiline && (r == '\n' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// Parse checks one item as it arrives. leaseID is the identifier the lease
// was issued under, or empty when there is no lease. The result is the item,
// or the one rule that refused or paused it. Broken data (E_PARSE) is
// reported ahead of unfamiliar data (E_NEEDS_SUBTOOL).
func Parse(leaseID string, payload []byte) (Item, *rule.Error) {
	ref := ""
	if rule.SafeID(leaseID) {
		ref = leaseID
	}
	fail := func(id rule.ID) (Item, *rule.Error) { return Item{}, rule.Fail(id, ref) }

	if leaseID != "" && ref == "" {
		return fail(rule.IntakeIDInvalid)
	}
	if len(payload) > MaxPayload {
		return fail(rule.IntakePayloadTooLarge)
	}
	var it Item
	if err := strictjson.Decode(payload, &it); err != nil {
		return fail(rule.IntakeMalformed)
	}
	if it.ID == "" {
		return fail(rule.IntakeFieldMissing)
	}
	if !rule.SafeID(it.ID) {
		return fail(rule.IntakeIDInvalid)
	}
	if ref == "" {
		ref = it.ID
	} else if it.ID != ref {
		return fail(rule.IntakeIDMismatch)
	}
	if it.Kind == "" || it.State == "" || it.Lane == "" {
		return fail(rule.IntakeFieldMissing)
	}

	var unfamiliar rule.ID
	note := func(id rule.ID) {
		if unfamiliar == "" {
			unfamiliar = id
		}
	}

	switch it.Kind {
	case Candidate:
		if it.Type != "" || it.Target != "" || it.Body != "" {
			return fail(rule.IntakeFieldUnexpected)
		}
		if Blank(it.Title) {
			return fail(rule.IntakeFieldMissing)
		}
		if !ValidLine(it.Title, MaxLine) || !ValidText(it.ScreenReason, MaxText) {
			return fail(rule.IntakeTextInvalid)
		}
		switch it.State {
		case Scored:
			if it.ScreenReason != "" {
				return fail(rule.IntakeFieldUnexpected)
			}
			if it.Score == nil || it.Recommendation == "" {
				return fail(rule.IntakeFieldMissing)
			}
			if *it.Score < MinScore || *it.Score > MaxScore {
				return fail(rule.IntakeScoreOutOfRange)
			}
			switch it.Recommendation {
			case Approved, Linked, Parked, Rejected:
			default:
				note(rule.IntakeRecommendationUnfamiliar)
			}
		case ScreenedOut:
			if it.Score != nil || it.Recommendation != "" {
				return fail(rule.IntakeFieldUnexpected)
			}
			if Blank(it.ScreenReason) {
				return fail(rule.IntakeFieldMissing)
			}
		default:
			note(rule.IntakeStateUnfamiliar)
		}
	case Proposal:
		if it.Title != "" || it.Score != nil || it.Recommendation != "" || it.ScreenReason != "" {
			return fail(rule.IntakeFieldUnexpected)
		}
		if it.Type == "" || Blank(it.Target) || Blank(it.Body) {
			return fail(rule.IntakeFieldMissing)
		}
		if len(it.Body) > MaxProposal {
			return fail(rule.IntakeProposalTooLarge)
		}
		if !ValidLine(it.Target, MaxLine) || !ValidText(it.Body, MaxProposal) {
			return fail(rule.IntakeTextInvalid)
		}
		if it.State != PendingApproval {
			note(rule.IntakeStateUnfamiliar)
		}
		switch it.Type {
		case "DATA", "CONFIG", "ISSUE":
		default:
			note(rule.IntakeProposalTypeUnfamiliar)
		}
	default:
		return fail(rule.IntakeKindUnfamiliar)
	}

	switch it.Lane {
	case Urgent, Normal, Backfill:
	default:
		note(rule.IntakeLaneUnfamiliar)
	}
	if unfamiliar != "" {
		return fail(unfamiliar)
	}
	return it, nil
}
