// Package rule is the one list of rules Countersign applies, and the only
// place a public error is made.
//
// Every rule has an identifier, at most one of the three public error codes,
// and a fixed sentence in plain words. An error carries a rule and an item
// identifier and nothing else, so it cannot name a machine, an address or a
// credential. Each rule has saved examples under testdata/rules/<identifier>.
package rule

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Code is one of the three public error codes.
type Code string

const (
	// Timeout: something outside took too long.
	Timeout Code = "E_TIMEOUT"
	// Parse: the data was broken.
	Parse Code = "E_PARSE"
	// NeedsSubtool: the data was valid and unfamiliar.
	NeedsSubtool Code = "E_NEEDS_SUBTOOL"
)

// ID names a rule.
type ID string

// Rules for an item arriving from Muster.
const (
	IntakeAccept                   ID = "intake.accept"
	IntakeMalformed                ID = "intake.malformed"
	IntakePayloadTooLarge          ID = "intake.payload_too_large"
	IntakeIDInvalid                ID = "intake.id_invalid"
	IntakeFieldMissing             ID = "intake.field_missing"
	IntakeFieldUnexpected          ID = "intake.field_unexpected"
	IntakeScoreOutOfRange          ID = "intake.score_out_of_range"
	IntakeTextInvalid              ID = "intake.text_invalid"
	IntakeProposalTooLarge         ID = "intake.proposal_too_large"
	IntakeKindUnfamiliar           ID = "intake.kind_unfamiliar"
	IntakeStateUnfamiliar          ID = "intake.state_unfamiliar"
	IntakeRecommendationUnfamiliar ID = "intake.recommendation_unfamiliar"
	IntakeEditKindUnfamiliar       ID = "intake.edit_kind_unfamiliar"
	IntakeLaneUnfamiliar           ID = "intake.lane_unfamiliar"
	IntakeFieldUnfamiliar          ID = "intake.field_unfamiliar"
)

// Rules for the owner's decision.
const (
	DecisionApprove               ID = "decision.approve"
	DecisionLink                  ID = "decision.link"
	DecisionMerge                 ID = "decision.merge"
	DecisionPark                  ID = "decision.park"
	DecisionReject                ID = "decision.reject"
	DecisionOverride              ID = "decision.override"
	DecisionProposalApprove       ID = "decision.proposal_approve"
	DecisionProposalDecline       ID = "decision.proposal_decline"
	DecisionRelease               ID = "decision.release"
	DecisionMalformed             ID = "decision.malformed"
	DecisionItemUnknown           ID = "decision.item_unknown"
	DecisionOutcomeUnknown        ID = "decision.outcome_unknown"
	DecisionOutcomeNotAllowed     ID = "decision.outcome_not_allowed"
	DecisionTextInvalid           ID = "decision.text_invalid"
	DecisionFieldUnexpected       ID = "decision.field_unexpected"
	DecisionLinkToolMissing       ID = "decision.link_tool_missing"
	DecisionMergeParentMissing    ID = "decision.merge_parent_missing"
	DecisionParkBlockerMissing    ID = "decision.park_blocker_missing"
	DecisionRejectReasonMissing   ID = "decision.reject_reason_missing"
	DecisionOverrideReasonMissing ID = "decision.override_reason_missing"
)

// Rules for handing results to Muster and holding leases.
const (
	SpoolAcknowledged ID = "spool.acknowledged"
	SpoolTimeout      ID = "spool.timeout"
	SpoolRefused      ID = "spool.refused"
	SpoolNoLease      ID = "spool.no_lease"
	SpoolLeaseNotHeld ID = "spool.lease_not_held"
	SpoolResent       ID = "spool.resent"
	SpoolStale        ID = "spool.stale"
	LeaseTimeout      ID = "lease.timeout"
	LeaseRefused      ID = "lease.refused"
	LeaseLost         ID = "lease.lost"
	StoreUnavailable  ID = "store.unavailable"
)

// Info describes one rule. An empty Code means the rule accepts.
type Info struct {
	ID   ID
	Code Code
	Text string
}

var table = []Info{
	{IntakeAccept, "", "the item is held for the owner"},
	{IntakeMalformed, Parse, "the item or lease is not one well-formed JSON object of the expected shape"},
	{IntakePayloadTooLarge, Parse, "the item is larger than Countersign accepts"},
	{IntakeIDInvalid, Parse, "the item identifier does not have the form of its kind"},
	{IntakeFieldMissing, Parse, "the item lacks something its kind and state require"},
	{IntakeFieldUnexpected, Parse, "the item carries a field its kind and state do not allow"},
	{IntakeScoreOutOfRange, Parse, "the rubric score is not between 20 and 100"},
	{IntakeTextInvalid, Parse, "a text field is too long or holds control characters"},
	{IntakeProposalTooLarge, Parse, "the edit proposal is larger than 4 KB"},
	{IntakeKindUnfamiliar, NeedsSubtool, "there is no rule for this kind of item"},
	{IntakeStateUnfamiliar, NeedsSubtool, "there is no rule for an item in this state"},
	{IntakeRecommendationUnfamiliar, NeedsSubtool, "there is no rule for this recommendation"},
	{IntakeEditKindUnfamiliar, NeedsSubtool, "there is no rule for this kind of edit"},
	{IntakeLaneUnfamiliar, NeedsSubtool, "there is no rule for this priority lane"},
	{IntakeFieldUnfamiliar, NeedsSubtool, "the lease carries a field Countersign has no rule for"},

	{DecisionApprove, "", "the candidate is approved to be built"},
	{DecisionLink, "", "the catalog will point to an existing tool"},
	{DecisionMerge, "", "the candidate is folded into another tool"},
	{DecisionPark, "", "the candidate is parked with its blocker named"},
	{DecisionReject, "", "the candidate is rejected with its score and reason"},
	{DecisionOverride, "", "the screened_out candidate goes back to observed"},
	{DecisionProposalApprove, "", "the edit proposal is approved on the urgent lane"},
	{DecisionProposalDecline, "", "the edit proposal is declined and nothing is changed"},
	{DecisionRelease, "", "the stopped item is released to run again"},
	{DecisionMalformed, Parse, "the decision is not one well-formed JSON object of the expected shape"},
	{DecisionItemUnknown, Parse, "no item with that identifier is waiting for a decision"},
	{DecisionOutcomeUnknown, Parse, "the outcome is not one Countersign knows"},
	{DecisionOutcomeNotAllowed, Parse, "that outcome is not allowed for an item of this kind and state"},
	{DecisionTextInvalid, Parse, "a text field is too long or holds control characters"},
	{DecisionFieldUnexpected, Parse, "the decision carries a field that outcome does not take"},
	{DecisionLinkToolMissing, Parse, "a link needs the existing tool named"},
	{DecisionMergeParentMissing, Parse, "a merge needs the parent tool named"},
	{DecisionParkBlockerMissing, Parse, "a park needs the blocker named"},
	{DecisionRejectReasonMissing, Parse, "a rejection needs a written reason"},
	{DecisionOverrideReasonMissing, Parse, "an override needs a written reason"},

	{SpoolAcknowledged, "", "Muster confirmed the record"},
	{SpoolTimeout, Timeout, "Muster did not confirm in time; the record stays in the spool and is retried in order"},
	{SpoolRefused, Parse, "Muster refused the record; the item stays in its current state"},
	{SpoolNoLease, Parse, "the record is for an item that was added by hand and has no lease, so it cannot be sent"},
	{SpoolLeaseNotHeld, Parse, "the lease is no longer held; the decision is kept and goes out again when Muster offers the item again"},
	{SpoolResent, "", "the item was leased again, so the waiting decision goes out under the new lease"},
	{SpoolStale, Parse, "the item came back changed, so the waiting decision no longer applies"},
	{LeaseTimeout, Timeout, "Muster did not answer a lease or heartbeat in time"},
	{LeaseRefused, Parse, "Muster refused a lease or heartbeat call; nothing was changed"},
	{LeaseLost, "", "the lease on a waiting item ended, so it left the inbox"},
	{StoreUnavailable, Timeout, "the local spool could not be read or written"},
}

var index = func() map[ID]Info {
	m := make(map[ID]Info, len(table))
	for _, info := range table {
		m[info.ID] = info
	}
	return m
}()

// All returns every rule, in a fixed order.
func All() []Info {
	out := make([]Info, len(table))
	copy(out, table)
	return out
}

// Lookup returns the description of one rule.
func Lookup(id ID) (Info, bool) {
	info, ok := index[id]
	return info, ok
}

// MaxID is the longest item identifier accepted.
const MaxID = 64

// SafeID reports whether s is a plain identifier: a letter or digit, then
// letters, digits, dots, underscores or hyphens, at most MaxID bytes in all.
func SafeID(s string) bool {
	if len(s) == 0 || len(s) > MaxID {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case i > 0 && (c == '.' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// Error is a public error: a rule, and the item it was applied to if that
// item has a safe identifier.
type Error struct {
	Rule ID
	Item string
}

// Fail makes the public error for a rule. An identifier that is not safe to
// print is left out.
func Fail(id ID, item string) *Error {
	if !SafeID(item) {
		item = ""
	}
	return &Error{Rule: id, Item: item}
}

// Code returns the public error code of the rule.
func (e *Error) Code() Code { return index[e.Rule].Code }

// Fingerprint is a short stable digest of the code, the rule and the item.
// The same failure on the same item always has the same fingerprint, on any
// machine.
func (e *Error) Fingerprint() string {
	sum := sha256.Sum256([]byte(string(e.Code()) + "\x00" + string(e.Rule) + "\x00" + e.Item))
	return hex.EncodeToString(sum[:8])
}

// Error is the whole public line.
func (e *Error) Error() string {
	s := string(e.Code()) + " " + string(e.Rule)
	if e.Item != "" {
		s += " " + e.Item
	}
	return s + ": " + index[e.Rule].Text + " [fp " + e.Fingerprint() + "]"
}

// Public turns any error into a public one. An error that already is public
// passes through. Anything else is replaced, whole, by the fallback rule: its
// own text is dropped because it may name a path, a host or an address.
func Public(err error, fallback ID, item string) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return Fail(fallback, item)
}
