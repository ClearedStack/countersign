// Package item describes what Countersign holds for the owner: scored and
// screened_out candidates, and pending edit proposals. It checks an item
// arriving from Muster and holds the table of moves the owner may make.
//
// Everything here is a pure function of its input.
package item

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// Kind is what sort of step this is. The values are Muster's: a candidate
// tool is an "item", an edit proposal is a "proposal", and an item a worker
// stopped with E_PARSE comes to the owner as a "release".
type Kind string

const (
	Candidate Kind = "item"
	Proposal  Kind = "proposal"
	Release   Kind = "release"
)

// State is an item's state. The candidate states are the ones the platform's
// item lifecycle names. Declined is provisional: the specification says a
// declined proposal is logged and does not give the state a name.
type State string

const (
	Discovered      State = "discovered"
	Recorded        State = "recorded"
	Observed        State = "observed"
	ScreenedOut     State = "screened_out"
	Scored          State = "scored"
	Approved        State = "approved"
	Building        State = "building"
	Live            State = "live"
	Parked          State = "parked"
	Linked          State = "linked"
	Merged          State = "merged"
	Rejected        State = "rejected"
	Retired         State = "retired"
	NeedsSubtool    State = "needs_subtool"
	PendingApproval State = "pending_approval"
	Declined        State = "declined"
)

// itemStates are the fourteen states an item can be in, and so the states a
// held item can be held in.
var itemStates = []State{
	Discovered, Recorded, Observed, ScreenedOut, Scored, Approved, Building,
	Live, Parked, Linked, Merged, Rejected, Retired, NeedsSubtool,
}

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
	// For a held item: let it run again, or leave it held.
	LetGo Outcome = "release"
	Keep  Outcome = "keep"
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

func init() {
	// A release step changes no state: the item keeps the state it is held
	// in, whichever of the fourteen that is.
	for _, s := range itemStates {
		moves[place{Release, s}] = []move{{LetGo, s}, {Keep, s}}
	}
}

// KnownOutcome reports whether o is an outcome at all.
func KnownOutcome(o Outcome) bool {
	switch o {
	case Approve, Link, Merge, Park, Reject, Override, Decline, LetGo, Keep:
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

// Limits. A proposal's 4 KB is read as 4096 bytes of its change and reason.
const (
	MaxPayload  = 32 << 10 // one item or decision, as typed or stored
	MaxLine     = 256      // a target, a tool, a parent, a repository, a file
	MaxTitle    = 512      // what a candidate does, in one sentence
	MaxText     = 4096     // a reason, a blocker
	MaxProposal = 4096     // the change and reason of an edit proposal, together
	MinScore    = 20
	MaxScore    = 100
)

// Identifiers are Muster's: PA- for a candidate and EP- for an edit
// proposal, then four digits, or five to nine digits with no leading zero.
var (
	candidateID = regexp.MustCompile(`^PA-([0-9]{4}|[1-9][0-9]{4,8})$`)
	proposalID  = regexp.MustCompile(`^EP-([0-9]{4}|[1-9][0-9]{4,8})$`)
)

// ValidID reports whether id has the form Muster gives that kind of item.
func ValidID(kind Kind, id string) bool {
	switch kind {
	case Candidate, Release:
		return candidateID.MatchString(id)
	case Proposal:
		return proposalID.MatchString(id)
	}
	return false
}

// Item is one thing waiting for the owner.
type Item struct {
	ID    string `json:"id"`
	Kind  Kind   `json:"kind"`
	State State  `json:"state"`
	Lane  Lane   `json:"lane"`

	// Candidates. Title is what the tool does, when an earlier worker said.
	Title            string `json:"title,omitempty"`
	Score            *int   `json:"score,omitempty"`
	Recommendation   State  `json:"recommendation,omitempty"`
	SplitMergeReview bool   `json:"split_merge_review,omitempty"`
	ScreenReason     string `json:"screen_reason,omitempty"`

	// Held items. The worker that stopped, and the code it gave.
	HeldWorker string `json:"held_worker,omitempty"`
	HeldCode   string `json:"held_code,omitempty"`

	// Edit proposals. The names are the ones Muster uses.
	Target     string `json:"target,omitempty"`
	Repository string `json:"repository,omitempty"`
	File       string `json:"file,omitempty"`
	EditKind   string `json:"edit_kind,omitempty"`
	Change     string `json:"change,omitempty"`
	Reason     string `json:"reason,omitempty"`
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

// Parse reads one item written as JSON in the form Countersign stores, and
// checks it.
func Parse(payload []byte) (Item, *rule.Error) {
	if len(payload) > MaxPayload {
		return Item{}, rule.Fail(rule.IntakePayloadTooLarge, "")
	}
	var it Item
	if err := strictjson.Decode(payload, &it); err != nil {
		return Item{}, rule.Fail(rule.IntakeMalformed, "")
	}
	if perr := Check(it); perr != nil {
		return Item{}, perr
	}
	return it, nil
}

// Check applies every rule for an arriving item. The result is nil, or the
// one rule that refuses or pauses the item. Broken data (E_PARSE) is
// reported ahead of unfamiliar data (E_NEEDS_SUBTOOL).
func Check(it Item) *rule.Error {
	ref := ""
	if rule.SafeID(it.ID) {
		ref = it.ID
	}
	fail := func(id rule.ID) *rule.Error { return rule.Fail(id, ref) }

	if it.ID == "" {
		return fail(rule.IntakeFieldMissing)
	}
	if ref == "" {
		return fail(rule.IntakeIDInvalid)
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

	proposalFields := it.Target != "" || it.Repository != "" || it.File != "" || it.EditKind != "" || it.Change != "" || it.Reason != ""
	scoreFields := it.Score != nil || it.Recommendation != "" || it.SplitMergeReview || it.ScreenReason != ""
	heldFields := it.HeldWorker != "" || it.HeldCode != ""

	switch it.Kind {
	case Candidate:
		if !ValidID(Candidate, it.ID) {
			return fail(rule.IntakeIDInvalid)
		}
		if proposalFields || heldFields {
			return fail(rule.IntakeFieldUnexpected)
		}
		if !ValidLine(it.Title, MaxTitle) || !ValidText(it.ScreenReason, MaxText) {
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
			if it.Score != nil || it.Recommendation != "" || it.SplitMergeReview {
				return fail(rule.IntakeFieldUnexpected)
			}
			if Blank(it.ScreenReason) {
				return fail(rule.IntakeFieldMissing)
			}
		default:
			note(rule.IntakeStateUnfamiliar)
		}
	case Proposal:
		if !ValidID(Proposal, it.ID) {
			return fail(rule.IntakeIDInvalid)
		}
		if it.Title != "" || scoreFields || heldFields {
			return fail(rule.IntakeFieldUnexpected)
		}
		if it.EditKind == "" || Blank(it.Target) || Blank(it.Change) {
			return fail(rule.IntakeFieldMissing)
		}
		if len(it.Change)+len(it.Reason) > MaxProposal {
			return fail(rule.IntakeProposalTooLarge)
		}
		if !ValidLine(it.Target, MaxLine) || !ValidLine(it.Repository, MaxLine) || !ValidLine(it.File, MaxLine) ||
			!ValidText(it.Change, MaxProposal) || !ValidText(it.Reason, MaxProposal) {
			return fail(rule.IntakeTextInvalid)
		}
		if it.State != PendingApproval {
			note(rule.IntakeStateUnfamiliar)
		}
		switch it.EditKind {
		case "DATA", "CONFIG", "ISSUE":
		default:
			note(rule.IntakeEditKindUnfamiliar)
		}
	case Release:
		if !ValidID(Release, it.ID) {
			return fail(rule.IntakeIDInvalid)
		}
		if proposalFields || scoreFields {
			return fail(rule.IntakeFieldUnexpected)
		}
		if it.HeldWorker == "" || it.HeldCode == "" {
			return fail(rule.IntakeFieldMissing)
		}
		if !ValidLine(it.Title, MaxTitle) || !rule.SafeID(it.HeldWorker) || !rule.SafeID(it.HeldCode) {
			return fail(rule.IntakeTextInvalid)
		}
		known := false
		for _, s := range itemStates {
			known = known || s == it.State
		}
		if !known {
			note(rule.IntakeStateUnfamiliar)
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
	return nil
}
