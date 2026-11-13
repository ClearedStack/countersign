package muster

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
	"github.com/ClearedStack/countersign/internal/strictjson"
)

// Rules the stand-in applies when it refuses a record. They stay inside this
// package: what Countersign reports is its own rule, spool.refused.
const refusedByStandIn = rule.SpoolRefused

// LeaseTTL is how long the stand-in's leases last without a heartbeat.
const LeaseTTL = 5 * time.Minute

// Seed is the made-up content a stand-in starts with.
type Seed struct {
	// Ownership lists the targets an edit proposal may name. It stands in
	// for ownership_map.json.
	Ownership []string `json:"ownership"`
	// Items are item payloads. Each must be a JSON object with an "id".
	Items []json.RawMessage `json:"items"`
	// Raw are payloads handed out exactly as written, under the given
	// identifier, for trying broken input.
	Raw []RawItem `json:"raw"`
	// Timeouts is how many calls fail before any succeeds.
	Timeouts int `json:"timeouts"`
	// LostAcks is how many records are applied and then reported as failed,
	// as when the confirmation is lost on the way back.
	LostAcks int `json:"lost_acks"`
}

// RawItem is a payload with the identifier its lease is issued under.
type RawItem struct {
	ID      string `json:"id"`
	Payload string `json:"payload"`
}

// Status of an item inside the stand-in.
const (
	StatusWaiting  = "waiting"   // can be leased
	StatusLeased   = "leased"    // held by Countersign
	StatusHeldBack = "held_back" // reported as unreadable; state unchanged
	StatusClosed   = "closed"    // decided or paused
)

type entry struct {
	id      string
	payload []byte
	state   string
	lane    string
	parsed  *item.Item
	until   time.Time
	status  string
}

// Dispatch is work the stand-in would hand to another worker.
type Dispatch struct {
	Item   string    `json:"item"`
	Worker string    `json:"worker"`
	Lane   item.Lane `json:"lane"`
}

// ItemView is one item as the stand-in sees it.
type ItemView struct {
	ID     string `json:"id"`
	State  string `json:"state,omitempty"`
	Lane   string `json:"lane,omitempty"`
	Status string `json:"status"`
}

// Snapshot is everything the stand-in holds, for tests and saved examples.
// Ledger stands in for what the public views would show.
type Snapshot struct {
	Items      []ItemView      `json:"items"`
	Ledger     []record.Record `json:"ledger"`
	Dispatched []Dispatch      `json:"dispatched"`
}

// StandIn is an in-memory Muster. It is safe for concurrent use.
type StandIn struct {
	mu         sync.Mutex
	now        func() time.Time
	items      []*entry
	ownership  map[string]bool
	seen       map[string]bool
	ledger     []record.Record
	dispatched []Dispatch
	timeouts   int
	lostAcks   int
}

// ErrSeed is returned when a seed cannot be used.
var ErrSeed = errors.New("muster: the stand-in seed is not usable")

// NewStandIn builds a stand-in from a seed. now is the clock it uses.
func NewStandIn(seed Seed, now func() time.Time) (*StandIn, error) {
	s := &StandIn{
		now:       now,
		ownership: map[string]bool{},
		seen:      map[string]bool{},
		timeouts:  seed.Timeouts,
		lostAcks:  seed.LostAcks,
	}
	for _, t := range seed.Ownership {
		s.ownership[t] = true
	}
	ids := map[string]bool{}
	add := func(id string, payload []byte) error {
		if !rule.SafeID(id) || ids[id] {
			return ErrSeed
		}
		ids[id] = true
		e := &entry{id: id, payload: payload, status: StatusWaiting}
		var peek struct {
			State string `json:"state"`
			Lane  string `json:"lane"`
		}
		if json.Unmarshal(payload, &peek) == nil {
			e.state, e.lane = peek.State, peek.Lane
		}
		if it, perr := item.Parse(id, payload); perr == nil {
			e.parsed = &it
		}
		s.items = append(s.items, e)
		return nil
	}
	for _, raw := range seed.Items {
		var peek struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &peek); err != nil {
			return nil, ErrSeed
		}
		if err := add(peek.ID, []byte(raw)); err != nil {
			return nil, err
		}
	}
	for _, r := range seed.Raw {
		if err := add(r.ID, []byte(r.Payload)); err != nil {
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

// SetFaults changes how many of the next calls time out, and how many of the
// next records are applied without the confirmation getting back.
func (s *StandIn) SetFaults(timeouts, lostAcks int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeouts, s.lostAcks = timeouts, lostAcks
}

// errUnreachable is what a real network failure looks like: it names an
// address. The address is from a range reserved for documentation. Nothing
// Countersign prints may ever contain it.
var errUnreachable = errors.New("dial tcp 192.0.2.7:8443: i/o timeout")

// LeakMarker is the text that must never appear in Countersign's output.
const LeakMarker = "192.0.2.7"

func (s *StandIn) down() bool {
	if s.timeouts > 0 {
		s.timeouts--
		return true
	}
	return false
}

func (s *StandIn) find(id string) *entry {
	for _, e := range s.items {
		if e.id == id {
			return e
		}
	}
	return nil
}

// Lease implements Queue.
func (s *StandIn) Lease(ctx context.Context, max int) ([]Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.down() {
		return nil, errUnreachable
	}
	now := s.now()
	var free []*entry
	for _, e := range s.items {
		if e.status == StatusLeased && !now.Before(e.until) {
			e.status = StatusWaiting // the lease ran out
		}
		if e.status == StatusWaiting {
			free = append(free, e)
		}
	}
	sort.SliceStable(free, func(i, j int) bool {
		return item.LaneRank(item.Lane(free[i].lane)) < item.LaneRank(item.Lane(free[j].lane))
	})
	var out []Lease
	for _, e := range free {
		if len(out) >= max {
			break
		}
		e.status, e.until = StatusLeased, now.Add(LeaseTTL)
		out = append(out, Lease{ID: e.id, Payload: append([]byte(nil), e.payload...)})
	}
	return out, nil
}

// Heartbeat implements Queue.
func (s *StandIn) Heartbeat(ctx context.Context, ids []string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.down() {
		return nil, errUnreachable
	}
	now := s.now()
	var held []string
	for _, id := range ids {
		e := s.find(id)
		if e == nil || e.status != StatusLeased || !now.Before(e.until) {
			continue
		}
		e.until = now.Add(LeaseTTL)
		held = append(held, id)
	}
	return held, nil
}

// Complete implements Queue.
func (s *StandIn) Complete(ctx context.Context, rec record.Record) error {
	return s.take(ctx, rec, record.Complete, s.complete)
}

// Fail implements Queue.
func (s *StandIn) Fail(ctx context.Context, rec record.Record) error {
	return s.take(ctx, rec, record.Fail, func(e *entry, rec record.Record) error {
		if rec.Code != string(rule.Parse) {
			return refuse(rec)
		}
		e.status = StatusHeldBack // state unchanged, not handed out again
		return nil
	})
}

// NeedsSubtool implements Queue.
func (s *StandIn) NeedsSubtool(ctx context.Context, rec record.Record) error {
	return s.take(ctx, rec, record.NeedsSubtool, func(e *entry, rec record.Record) error {
		if rec.Code != string(rule.NeedsSubtool) || rec.To != item.NeedsSubtool {
			return refuse(rec)
		}
		e.state, e.status = string(item.NeedsSubtool), StatusClosed
		return nil
	})
}

func refuse(rec record.Record) error { return rule.Fail(refusedByStandIn, rec.Item) }

// take is the part every record call shares: outages, the checks on the
// envelope, and treating a record seen before as already confirmed.
func (s *StandIn) take(ctx context.Context, rec record.Record, call string, apply func(*entry, record.Record) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.down() {
		return errUnreachable
	}
	if rec.Call != call || !rec.Sealed() {
		return refuse(rec)
	}
	if s.seen[rec.ID] {
		return nil
	}
	e := s.find(rec.Item)
	if e == nil || e.status == StatusClosed {
		return refuse(rec)
	}
	if err := apply(e, rec); err != nil {
		return err
	}
	s.seen[rec.ID] = true
	s.ledger = append(s.ledger, rec)
	if s.lostAcks > 0 {
		s.lostAcks--
		return errUnreachable
	}
	return nil
}

// complete checks a decision the way the specification says Muster does, and
// applies it.
func (s *StandIn) complete(e *entry, rec record.Record) error {
	it := e.parsed
	if it == nil || rec.Kind != it.Kind || string(rec.From) != e.state {
		return refuse(rec)
	}
	to, ok := item.Next(it.Kind, it.State, rec.Outcome)
	if !ok || to != rec.To {
		return refuse(rec)
	}
	// An override needs a written reason.
	override := it.State == item.ScreenedOut || (it.State == item.Scored && to != it.Recommendation)
	if override != rec.Override || (override && strings.TrimSpace(rec.Reason) == "") {
		return refuse(rec)
	}
	lane := it.Lane
	if it.Kind == item.Proposal {
		// The target must be in the ownership map.
		if !s.ownership[it.Target] {
			return refuse(rec)
		}
		if rec.Outcome == item.Approve {
			lane = item.Urgent
		}
	}
	if rec.Lane != lane {
		return refuse(rec)
	}
	e.state, e.lane, e.status = string(to), string(lane), StatusClosed
	if it.Kind == item.Proposal && rec.Outcome == item.Approve {
		// An approved edit is handed to Bellbuoy on the urgent lane. A
		// declined one changes nothing.
		s.dispatched = append(s.dispatched, Dispatch{Item: e.id, Worker: "bellbuoy", Lane: item.Urgent})
	}
	return nil
}

// Snapshot returns a copy of everything the stand-in holds.
func (s *StandIn) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{
		Items:      make([]ItemView, 0, len(s.items)),
		Ledger:     append([]record.Record{}, s.ledger...),
		Dispatched: append([]Dispatch{}, s.dispatched...),
	}
	now := s.now()
	for _, e := range s.items {
		status := e.status
		if status == StatusLeased && !now.Before(e.until) {
			status = StatusWaiting
		}
		snap.Items = append(snap.Items, ItemView{ID: e.id, State: e.state, Lane: e.lane, Status: status})
	}
	return snap
}
