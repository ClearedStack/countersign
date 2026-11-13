// Package queue holds the rules of Countersign: which items it holds, which
// outcomes a person may record for them, and when a decision is refused.
// Nothing in this package reads a clock, a file or the network.
package queue

import (
	"bytes"
	"encoding/json"

	"github.com/clearedstack/countersign/internal/fault"
)

// Item kinds.
const (
	KindCandidate    = "candidate"
	KindEditProposal = "edit_proposal"
)

// States an item may be in when it is leased.
const (
	StateScored          = "scored"
	StateScreenedOut     = "screened-out"
	StatePendingApproval = "pending_approval"
)

// PausedNeedsSubtool marks an item that is valid but that no rule covers.
const PausedNeedsSubtool = "needs_subtool"

// MaxProposalBytes is the largest edit proposal body that is held.
const MaxProposalBytes = 4096

// MaxIDBytes is the longest item id that is held.
const MaxIDBytes = 128

// Item is one thing waiting for a person to decide.
type Item struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	State string `json:"state"`
	Title string `json:"title,omitempty"`
	// Score is the rubric score of a candidate.
	Score *int `json:"score,omitempty"`
	// Type, Target and Body describe an edit proposal.
	Type   string `json:"type,omitempty"`
	Target string `json:"target,omitempty"`
	Body   string `json:"body,omitempty"`
	// Paused is "needs_subtool" when the item is valid but unfamiliar.
	// A paused item is held and shown, and no decision is accepted for it.
	Paused string `json:"paused,omitempty"`
}

var proposalTypes = map[string]bool{"DATA": true, "CONFIG": true, "ISSUE": true}

// Admit reads one leased payload and decides whether Countersign holds it.
//
// A broken payload is refused with E_PARSE. A payload that is valid but that
// no rule covers is returned with Paused set, so it is held and reported
// instead of being guessed at.
func Admit(raw []byte) (Item, *fault.Error) {
	var fields map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&fields); err != nil || fields == nil || dec.More() {
		return Item{}, fault.New(fault.Parse, "item-not-an-object", "a leased item is not a JSON object")
	}

	var it Item
	unfamiliar := false
	for name, value := range fields {
		var err error
		switch name {
		case "id":
			err = json.Unmarshal(value, &it.ID)
		case "kind":
			err = json.Unmarshal(value, &it.Kind)
		case "state":
			err = json.Unmarshal(value, &it.State)
		case "title":
			err = json.Unmarshal(value, &it.Title)
		case "score":
			err = strictInt(value, &it.Score)
		case "type":
			err = json.Unmarshal(value, &it.Type)
		case "target":
			err = json.Unmarshal(value, &it.Target)
		case "body":
			err = json.Unmarshal(value, &it.Body)
		default:
			unfamiliar = true
		}
		if err != nil {
			return Item{}, fault.New(fault.Parse, "item-field-type", "a field of a leased item has the wrong type")
		}
	}

	if !validID(it.ID) {
		return Item{}, fault.New(fault.Parse, "item-id", "a leased item has no usable id")
	}
	if it.Kind == "" || it.State == "" {
		return Item{}, fault.New(fault.Parse, "item-kind-state", "a leased item has no kind or no state")
	}

	switch {
	case it.Kind == KindCandidate && (it.State == StateScored || it.State == StateScreenedOut):
	case it.Kind == KindEditProposal && it.State == StatePendingApproval:
		if it.Type == "" {
			return Item{}, fault.New(fault.Parse, "proposal-type-missing", "an edit proposal has no type")
		}
		if len(it.Body) > MaxProposalBytes {
			return Item{}, fault.New(fault.Parse, "proposal-too-large", "an edit proposal is larger than 4 KB")
		}
		if !proposalTypes[it.Type] {
			unfamiliar = true
		}
	default:
		unfamiliar = true
	}

	if unfamiliar {
		it.Paused = PausedNeedsSubtool
	}
	return it, nil
}

// strictInt accepts a JSON whole number and nothing else.
func strictInt(value json.RawMessage, into **int) error {
	var n int
	if err := json.Unmarshal(value, &n); err != nil {
		return err
	}
	*into = &n
	return nil
}

// validID accepts printable ASCII without spaces, so an id can be typed on a
// command line and printed in a column.
func validID(id string) bool {
	if id == "" || len(id) > MaxIDBytes {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] <= ' ' || id[i] > '~' {
			return false
		}
	}
	return true
}
