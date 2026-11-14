package muster

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// MaxLeaseBody is the largest answer to a lease call that is read. Muster's
// own requests are at most 64 KiB, and an answer carries the latest result
// of each earlier worker.
const MaxLeaseBody = 256 << 10

// Lease is Muster's answer to a lease call.
type Lease struct {
	Token    string                     `json:"lease"`
	Kind     string                     `json:"kind"`
	ID       string                     `json:"id"`
	State    string                     `json:"state"`
	Lane     string                     `json:"lane"`
	Attempt  int                        `json:"attempt"`
	Expires  string                     `json:"expires"`
	Found    *Found                     `json:"found,omitempty"`
	Results  map[string]json.RawMessage `json:"results,omitempty"`
	Proposal *Proposal                  `json:"proposal,omitempty"`
	// Incident is for Bellbuoy. Countersign has no rule for an incident
	// step, and the field is read only so that its shape is not an error.
	Incident json.RawMessage `json:"incident,omitempty"`
}

// Found is how an item was recorded: where it was found, and its title and
// address as they were given.
type Found struct {
	Source  string `json:"source"`
	Title   string `json:"title"`
	Address string `json:"address"`
}

// Proposal is an edit proposal as Muster hands it over.
type Proposal struct {
	Target     string `json:"target"`
	Repository string `json:"repository"`
	File       string `json:"file"`
	EditKind   string `json:"edit_kind"`
	Change     string `json:"change"`
	Reason     string `json:"reason"`
}

var leaseFields = map[string]bool{
	"lease": true, "kind": true, "id": true, "state": true, "lane": true,
	"attempt": true, "expires": true, "found": true, "results": true,
	"proposal": true, "incident": true,
}

// A lease token is Muster's to choose, and is handed back as it was given.
// The ones it gives look like "PA-0001.7.2" and "incident:PA-0001.8.1".
var tokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// ValidToken reports whether s can be a lease token: short, plain, and safe
// to store and send back.
func ValidToken(s string) bool { return tokenPattern.MatchString(s) }

// DecodeLease reads the body of a lease answer. On failure the returned
// Lease still carries the token when one could be read, so that the lease
// can be ended instead of left to run out.
func DecodeLease(body []byte) (Lease, *rule.Error) {
	var l Lease
	if len(body) > MaxLeaseBody {
		return l, rule.Fail(rule.IntakePayloadTooLarge, "")
	}
	// Read what can be read of the token and identifier first.
	var peek struct {
		Token string `json:"lease"`
		ID    string `json:"id"`
	}
	if utf8.Valid(body) && json.Unmarshal(body, &peek) == nil && ValidToken(peek.Token) {
		l.Token = peek.Token
	}
	ref := peek.ID

	var fields map[string]json.RawMessage
	if err := strictjson.Decode(body, &fields); err != nil || fields == nil {
		return l, rule.Fail(rule.IntakeMalformed, ref)
	}
	unfamiliar := false
	for name := range fields {
		unfamiliar = unfamiliar || !leaseFields[name]
	}
	if unfamiliar {
		// Valid JSON with a field no rule covers. Its meaning is not guessed.
		return l, rule.Fail(rule.IntakeFieldUnfamiliar, ref)
	}
	var full Lease
	if err := strictjson.Decode(body, &full); err != nil {
		return l, rule.Fail(rule.IntakeMalformed, ref)
	}
	if !ValidToken(full.Token) {
		return l, rule.Fail(rule.IntakeMalformed, ref)
	}
	return full, nil
}

// evaluation is the part of Assay's result that Countersign reads. Its form
// is the one Muster's contract names for an evaluation, which is Logbook's:
// the score, the recommendation, the split flag, and a verdict for each gate
// by name. The rest of the result is left alone.
type evaluation struct {
	Score          *int   `json:"score"`
	Recommendation string `json:"recommendation"`
	SplitFlag      bool   `json:"split_flag"`
	Gates          map[string]struct {
		Passed *bool  `json:"passed"`
		Reason string `json:"reason"`
	} `json:"gates"`
}

// sextantResult is the one fact Countersign reads from Sextant's result:
// what the tool does.
type sextantResult struct {
	CoreJob struct {
		Value *string `json:"value"`
	} `json:"core_job"`
}

// recommendations maps Assay's four recommendations to the state each one
// recommends.
var recommendations = map[string]item.State{
	"build":  item.Approved,
	"link":   item.Linked,
	"park":   item.Parked,
	"reject": item.Rejected,
}

// Item turns a lease into the item the owner is shown, and checks it. The
// result is the item, or the one rule that refuses or pauses it.
func (l Lease) Item() (item.Item, *rule.Error) {
	it := item.Item{ID: l.ID, Kind: item.Kind(l.Kind), State: item.State(l.State), Lane: item.Lane(l.Lane)}
	fail := func(id rule.ID) (item.Item, *rule.Error) { return item.Item{}, rule.Fail(id, l.ID) }

	// What the tool is called: the title it was recorded with, or else what
	// Sextant found it does.
	title := func() *rule.Error {
		if l.Found != nil && l.Found.Title != "" {
			it.Title = l.Found.Title
			return nil
		}
		if raw, ok := l.Results["sextant"]; ok {
			var s sextantResult
			if json.Unmarshal(raw, &s) != nil {
				return rule.Fail(rule.IntakeMalformed, l.ID)
			}
			if s.CoreJob.Value != nil {
				it.Title = *s.CoreJob.Value
			}
		}
		return nil
	}

	switch it.Kind {
	case item.Candidate:
		if l.Proposal != nil {
			return fail(rule.IntakeFieldUnexpected)
		}
		if perr := title(); perr != nil {
			return item.Item{}, perr
		}
		if it.State != item.Scored && it.State != item.ScreenedOut {
			break // Check reports the unfamiliar state
		}
		raw, ok := l.Results["assay"]
		if !ok {
			return fail(rule.IntakeFieldMissing)
		}
		var a evaluation
		if json.Unmarshal(raw, &a) != nil {
			return fail(rule.IntakeMalformed)
		}
		if it.State == item.Scored {
			it.Score = a.Score
			it.SplitFlag = a.SplitFlag
			if to, known := recommendations[a.Recommendation]; known {
				it.Recommendation = to
			} else {
				// Carried as it came, so that Check reports it: missing if
				// empty, unfamiliar otherwise.
				it.Recommendation = item.State(a.Recommendation)
			}
		} else {
			// Each failed gate, by name, in a fixed order.
			var failed []string
			for name, g := range a.Gates {
				if g.Passed != nil && !*g.Passed {
					failed = append(failed, name+": "+strings.TrimSpace(cleanLines(g.Reason)))
				}
			}
			sort.Strings(failed)
			it.ScreenReason = strings.Join(failed, "\n")
		}
	case item.Proposal:
		if l.Found != nil {
			return fail(rule.IntakeFieldUnexpected)
		}
		if l.Proposal == nil {
			return fail(rule.IntakeFieldMissing)
		}
		p := l.Proposal
		it.Target, it.Repository, it.File = p.Target, p.Repository, p.File
		it.EditKind, it.Change, it.Reason = p.EditKind, cleanLines(p.Change), cleanLines(p.Reason)
	}
	if perr := item.Check(it); perr != nil {
		return item.Item{}, perr
	}
	return it, nil
}

// cleanLines turns CRLF and lone CR line breaks into LF. Browser forms send
// CRLF, Muster's intake preserves it, and Logbook's text check allows CR.
func cleanLines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
