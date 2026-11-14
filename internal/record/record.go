// Package record is what Countersign hands to Muster: one record for each
// decision, each refused item and each paused item.
package record

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/ClearedStack/countersign/internal/item"
)

// ContractVersion is the version of the contract with Muster.
const ContractVersion = "v1"

// Worker is this component's name in the registry.
const Worker = "countersign"

// The calls that carry a record. The names are Muster's.
const (
	Complete     = "complete"
	Fail         = "fail"
	NeedsSubtool = "needs_subtool"
	Release      = "release"
)

// Record is one result. ID is a digest of everything else in it, so sending
// the same record twice is harmless and recognisable.
type Record struct {
	Contract string `json:"contract"`
	Call     string `json:"call"`
	ID       string `json:"id"`
	Worker   string `json:"worker"`
	Item     string `json:"item"`
	At       string `json:"at"`

	// A decision (call "complete").
	Kind           item.Kind    `json:"kind,omitempty"`
	Outcome        item.Outcome `json:"outcome,omitempty"`
	From           item.State   `json:"from,omitempty"`
	To             item.State   `json:"to,omitempty"`
	Lane           item.Lane    `json:"lane,omitempty"`
	Override       bool         `json:"override,omitempty"`
	Score          *int         `json:"score,omitempty"`
	Recommendation item.State   `json:"recommendation,omitempty"`
	Reason         string       `json:"reason,omitempty"`
	Tool           string       `json:"tool,omitempty"`
	Parent         string       `json:"parent,omitempty"`
	Blocker        string       `json:"blocker,omitempty"`

	// A refusal or a pause (calls "fail" and "needs_subtool").
	Code        string `json:"code,omitempty"`
	Rule        string `json:"rule,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Stamp formats a time the one way records carry it.
func Stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// Seal fills in the fixed fields and the identifier.
func (r Record) Seal() Record {
	r.Contract = ContractVersion
	r.Worker = Worker
	r.ID = ""
	b, err := json.Marshal(r)
	if err != nil {
		// A Record holds only strings, a bool and an int: it always encodes.
		panic("record: cannot encode")
	}
	sum := sha256.Sum256(b)
	r.ID = hex.EncodeToString(sum[:16])
	return r
}

// Sealed reports whether the identifier matches the content.
func (r Record) Sealed() bool {
	return r.ID != "" && r.Seal().ID == r.ID && r.Contract == ContractVersion && r.Worker == Worker
}
