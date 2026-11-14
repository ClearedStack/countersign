package muster

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// LeaseTTL is how long the stand-in's leases last without a heartbeat. It
// is Muster's default.
const LeaseTTL = 60 * time.Second

// Seed is the made-up content a stand-in starts with.
type Seed struct {
	// Ownership lists the targets an edit proposal may name. It stands in
	// for ownership_map.json.
	Ownership []string `json:"ownership"`
	// Steps are the things waiting for Countersign, each written as Muster's
	// lease answer without "lease", "attempt" and "expires". Each must be a
	// JSON object with an "id".
	Steps []json.RawMessage `json:"steps"`
	// Raw are answers handed out exactly as written, for trying broken
	// input. The text {{lease}} is replaced by the lease token.
	Raw []RawStep `json:"raw"`
	// Timeouts is how many calls fail before any succeeds.
	Timeouts int `json:"timeouts"`
	// LostAcks is how many results are applied and then reported as failed,
	// as when the confirmation is lost on the way back.
	LostAcks int `json:"lost_acks"`
}

// RawStep is a lease answer given as text.
type RawStep struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}

// Status of a step inside the stand-in.
const (
	StatusWaiting = "waiting" // can be leased
	StatusLeased  = "leased"  // held by Countersign
	StatusHeld    = "held"    // stopped by E_PARSE: state unchanged, not offered again
	StatusClosed  = "closed"  // decided
)

type entry struct {
	id     string
	kind   string
	fields map[string]json.RawMessage // nil for a raw step
	raw    string
	state  string
	lane   string

	status  string
	token   string // the lease that counts now, if any
	until   time.Time
	attempt int
	parsed  *item.Item

	endToken string // the lease that ended the step
	endState string
}

// Dispatch is work the stand-in would hand to another worker.
type Dispatch struct {
	Item   string    `json:"item"`
	Worker string    `json:"worker"`
	Lane   item.Lane `json:"lane"`
}

// ItemView is one step as the stand-in sees it. Kind is given for a release
// step, which shares its identifier with the item it is about.
type ItemView struct {
	Kind   string `json:"kind,omitempty"`
	ID     string `json:"id"`
	State  string `json:"state,omitempty"`
	Lane   string `json:"lane,omitempty"`
	Status string `json:"status"`
}

// Accepted is one call that ended a lease, as the stand-in recorded it.
type Accepted struct {
	Call     string         `json:"call"`
	Lease    string         `json:"lease"`
	ID       string         `json:"id"`
	State    string         `json:"state,omitempty"`
	Code     string         `json:"code,omitempty"`
	Decision string         `json:"decision,omitempty"`
	Result   *record.Record `json:"result,omitempty"`
}

// Snapshot is everything the stand-in holds, for tests and saved examples.
// Ledger stands in for what the public views would be fed from.
type Snapshot struct {
	Items      []ItemView `json:"items"`
	Ledger     []Accepted `json:"ledger"`
	Dispatched []Dispatch `json:"dispatched"`
}

// StandIn is an in-memory Muster for Countersign's calls only. It follows
// Muster's contract v1 as far as Countersign sees it, and it checks a
// decision the way Countersign's specification says Muster does. It is safe
// for concurrent use.
type StandIn struct {
	mu         sync.Mutex
	now        func() time.Time
	items      []*entry
	ownership  map[string]bool
	leases     int
	ledger     []Accepted
	dispatched []Dispatch
	timeouts   int
	lostAcks   int
}

// ErrSeed is returned when a seed cannot be used.
var ErrSeed = errors.New("muster: the stand-in seed is not usable")

// NewStandIn builds a stand-in from a seed. now is the clock it uses.
func NewStandIn(seed Seed, now func() time.Time) (*StandIn, error) {
	s := &StandIn{now: now, ownership: map[string]bool{}, timeouts: seed.Timeouts, lostAcks: seed.LostAcks}
	for _, t := range seed.Ownership {
		s.ownership[t] = true
	}
	ids := map[string]bool{}
	add := func(e *entry) error {
		key := e.id
		if e.kind == string(item.Release) {
			key = "release:" + e.id
		}
		if !rule.SafeID(e.id) || ids[key] {
			return ErrSeed
		}
		ids[key] = true
		e.status = StatusWaiting
		s.items = append(s.items, e)
		return nil
	}
	for _, raw := range seed.Steps {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return nil, ErrSeed
		}
		e := &entry{fields: fields}
		json.Unmarshal(fields["id"], &e.id)
		json.Unmarshal(fields["kind"], &e.kind)
		json.Unmarshal(fields["state"], &e.state)
		json.Unmarshal(fields["lane"], &e.lane)
		if err := add(e); err != nil {
			return nil, err
		}
	}
	for _, r := range seed.Raw {
		if err := add(&entry{id: r.ID, raw: r.Body}); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// ParseSeed reads a seed from JSON.
func ParseSeed(data []byte) (Seed, error) {
	var seed Seed
	if err := strictjson.Decode(data, &seed); err != nil {
		return Seed{}, ErrSeed
	}
	return seed, nil
}

// SetFaults changes how many of the next calls go unanswered, and how many
// of the next results are applied without the confirmation getting back.
func (s *StandIn) SetFaults(timeouts, lostAcks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeouts, s.lostAcks = timeouts, lostAcks
}

func (s *StandIn) down(ctx context.Context) bool {
	if ctx.Err() != nil {
		return true
	}
	if s.timeouts > 0 {
		s.timeouts--
		return true
	}
	return false
}

func (s *StandIn) byToken(token string) *entry {
	if token == "" {
		return nil
	}
	for _, e := range s.items {
		if e.token == token || e.endToken == token {
			return e
		}
	}
	return nil
}

// expire lets go of leases that ran out. The old token still counts until
// someone takes the step again.
func (s *StandIn) expire() {
	now := s.now()
	for _, e := range s.items {
		if e.status == StatusLeased && !now.Before(e.until) {
			e.status = StatusWaiting
		}
	}
}

// Lease implements Queue.
func (s *StandIn) Lease(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down(ctx) {
		return nil, ErrUnreachable
	}
	s.expire()
	var free []*entry
	for _, e := range s.items {
		if e.status == StatusWaiting {
			free = append(free, e)
		}
	}
	if len(free) == 0 {
		return nil, nil
	}
	sort.SliceStable(free, func(i, j int) bool {
		return item.LaneRank(item.Lane(free[i].lane)) < item.LaneRank(item.Lane(free[j].lane))
	})
	e := free[0]
	s.leases++
	e.attempt++
	e.token = e.id + "." + strconv.Itoa(s.leases)
	if e.kind == string(item.Release) {
		e.token = "release:" + e.token
	}
	e.status, e.until = StatusLeased, s.now().Add(LeaseTTL)

	var body []byte
	if e.fields == nil {
		body = []byte(strings.ReplaceAll(e.raw, "{{lease}}", e.token))
	} else {
		out := make(map[string]any, len(e.fields)+3)
		for k, v := range e.fields {
			out[k] = v
		}
		out["lease"], out["attempt"], out["expires"] = e.token, e.attempt, record.Stamp(e.until)
		body, _ = json.Marshal(out)
	}
	e.parsed = nil
	if l, perr := DecodeLease(body); perr == nil {
		if it, perr := l.Item(); perr == nil {
			e.parsed = &it
		}
	}
	return body, nil
}

// Heartbeat implements Queue.
func (s *StandIn) Heartbeat(ctx context.Context, lease string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down(ctx) {
		return ErrUnreachable
	}
	s.expire()
	e := s.byToken(lease)
	if e == nil || e.token != lease || e.status != StatusLeased {
		return ErrRefused
	}
	e.until = s.now().Add(LeaseTTL)
	return nil
}

// end is the part shared by the calls that end a lease: outages, repeats,
// and losing the confirmation on the way back.
func (s *StandIn) end(ctx context.Context, lease, endState string, apply func(*entry) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down(ctx) {
		return ErrUnreachable
	}
	s.expire()
	e := s.byToken(lease)
	if e == nil {
		return ErrRefused
	}
	if e.endToken == lease {
		if e.endState == endState {
			return nil // the same call again: the same answer, nothing done twice
		}
		return ErrRefused
	}
	// A lease that ran out still counts until the step is taken again.
	if e.status != StatusLeased && e.status != StatusWaiting {
		return ErrRefused
	}
	if err := apply(e); err != nil {
		return err
	}
	e.endToken, e.endState, e.token = lease, endState, ""
	if s.lostAcks > 0 {
		s.lostAcks--
		return ErrUnreachable
	}
	return nil
}

// Complete implements Queue. It checks the decision for itself and does not
// trust that Countersign already did.
func (s *StandIn) Complete(ctx context.Context, lease string, state item.State, result []byte) error {
	if e := s.peek(lease); e != nil && e.kind == string(item.Release) {
		return s.release(ctx, lease, state, result)
	}
	return s.end(ctx, lease, "complete "+string(state), func(e *entry) error {
		it := e.parsed
		if it == nil {
			return ErrRefused
		}
		var rec record.Record
		if strictjson.Decode(result, &rec) != nil || !rec.Sealed() || rec.Call != record.Complete {
			return ErrRefused
		}
		if rec.Item != e.id || rec.Kind != it.Kind || string(rec.From) != e.state || rec.To != state {
			return ErrRefused
		}
		to, ok := item.Next(it.Kind, it.State, rec.Outcome)
		if !ok || to != state {
			return ErrRefused // that move is not allowed
		}
		// An override needs a written reason.
		override := it.State == item.ScreenedOut || (it.State == item.Scored && to != it.Recommendation)
		if override != rec.Override || (override && strings.TrimSpace(rec.Reason) == "") {
			return ErrRefused
		}
		lane := it.Lane
		if it.Kind == item.Proposal {
			if !s.ownership[it.Target] {
				return ErrRefused // the target is not in the ownership map
			}
			if rec.Outcome == item.Approve {
				lane = item.Urgent
			}
		}
		if rec.Lane != lane {
			return ErrRefused
		}
		e.state, e.lane, e.status = string(to), string(lane), StatusClosed
		s.ledger = append(s.ledger, Accepted{Call: record.Complete, Lease: lease, ID: e.id, State: e.state, Result: &rec})
		if it.Kind == item.Proposal && rec.Outcome == item.Approve {
			// An approved edit goes to Bellbuoy on the urgent lane. A
			// declined one changes nothing.
			s.dispatched = append(s.dispatched, Dispatch{Item: e.id, Worker: "bellbuoy", Lane: item.Urgent})
		}
		return nil
	})
}

// peek finds the step a lease belongs to, without changing anything.
func (s *StandIn) peek(lease string) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byToken(lease)
}

// release ends a release step with the owner's decision. The result must be
// exactly {"decision": "release"} or {"decision": "keep"}, and the state the
// one the step was given with.
func (s *StandIn) release(ctx context.Context, lease string, state item.State, result []byte) error {
	var r struct {
		Decision *string `json:"decision"`
	}
	bad := strictjson.Decode(result, &r) != nil || r.Decision == nil ||
		(*r.Decision != string(item.LetGo) && *r.Decision != string(item.Keep))
	decision := ""
	if !bad {
		decision = *r.Decision
	}
	return s.end(ctx, lease, "complete "+string(state)+" "+decision, func(e *entry) error {
		if bad || string(state) != e.state {
			return ErrRefused
		}
		e.status = StatusClosed
		s.ledger = append(s.ledger, Accepted{Call: record.Complete, Lease: lease, ID: e.id, State: e.state, Decision: decision})
		if decision != string(item.LetGo) {
			return nil // kept: nothing offers the item again
		}
		// The item's own step goes back to its worker, at the end of the
		// urgent lane.
		for i, held := range s.items {
			if held.id == e.id && held.kind != string(item.Release) && held.status == StatusHeld {
				held.status, held.lane = StatusWaiting, string(item.Urgent)
				if held.fields != nil {
					held.fields["lane"], _ = json.Marshal(held.lane)
				}
				s.items = append(append(s.items[:i:i], s.items[i+1:]...), held)
				break
			}
		}
		return nil
	})
}

// Fail implements Queue.
func (s *StandIn) Fail(ctx context.Context, lease string, code rule.Code) error {
	return s.end(ctx, lease, "fail "+string(code), func(e *entry) error {
		switch {
		case code == rule.Parse && e.kind == string(item.Release):
			return ErrRefused // a release step can only be put back
		case code == rule.Parse:
			e.status = StatusHeld // keeps its state, not offered again
			open := false
			for _, other := range s.items {
				open = open || (other.id == e.id && other.kind == string(item.Release) && other.status != StatusClosed)
			}
			if e.kind == string(item.Candidate) && !open {
				// The owner is given a release step for it.
				held, _ := json.Marshal(map[string]string{"worker": record.Worker, "code": string(rule.Parse)})
				results, _ := json.Marshal(map[string]json.RawMessage{"held": held})
				quote := func(v string) json.RawMessage { b, _ := json.Marshal(v); return b }
				s.items = append(s.items, &entry{
					id: e.id, kind: string(item.Release), state: e.state, lane: string(item.Normal), status: StatusWaiting,
					fields: map[string]json.RawMessage{
						"kind": quote(string(item.Release)), "id": quote(e.id), "state": quote(e.state),
						"lane": quote(string(item.Normal)), "results": results,
					},
				})
			}
		case code == rule.Timeout:
			e.status = StatusWaiting // stays in the queue to be tried again
		default:
			return ErrRefused
		}
		s.ledger = append(s.ledger, Accepted{Call: record.Fail, Lease: lease, ID: e.id, State: e.state, Code: string(code)})
		return nil
	})
}

// NeedsSubtool implements Queue. Muster's state table has no move to
// needs_subtool from any state Countersign holds: a scored item waits for a
// person, not for a rule, and a proposal cannot be paused. So the stand-in
// always refuses, as Muster does.
func (s *StandIn) NeedsSubtool(ctx context.Context, lease string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down(ctx) {
		return ErrUnreachable
	}
	return ErrRefused
}

// Snapshot returns a copy of everything the stand-in holds.
func (s *StandIn) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Items:      make([]ItemView, 0, len(s.items)),
		Ledger:     append([]Accepted{}, s.ledger...),
		Dispatched: append([]Dispatch{}, s.dispatched...),
	}
	now := s.now()
	for _, e := range s.items {
		status := e.status
		if status == StatusLeased && !now.Before(e.until) {
			status = StatusWaiting
		}
		view := ItemView{ID: e.id, State: e.state, Lane: e.lane, Status: status}
		if e.kind == string(item.Release) {
			view.Kind = e.kind
		}
		snap.Items = append(snap.Items, view)
	}
	return snap
}
