// Package store keeps Countersign's state on disk, under one directory: the
// items waiting for the owner, and the spool of records Muster has not yet
// confirmed. In the pod that directory is /var/spool/cs, the only writable
// path.
//
//	pending/<id>.json    an item waiting for a decision
//	outbox/<seq>.json    a record waiting for Muster to confirm it, in order
//	done/<seq>.json      a record Muster confirmed or refused (newest kept)
//	lock                 the lock every change is made under
//
// Every file is written whole to a temporary name, synced and renamed, so a
// stop at any moment leaves either the old state or the new one.
//
// The store's errors are public errors. A failure of the disk is reported as
// store.unavailable and its own text is dropped, because it names a path.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ClearedStack/countersign/internal/decide"
	"github.com/ClearedStack/countersign/internal/item"
	"github.com/ClearedStack/countersign/internal/record"
	"github.com/ClearedStack/countersign/internal/rule"
)

// KeepDone is how many confirmed or refused records are kept for the log.
// Muster holds the lasting record. This is only the recent local history.
const KeepDone = 200

// Status of a record in the log.
const (
	Waiting      = "waiting"
	Acknowledged = "acknowledged"
	Refused      = "refused"
)

// Held is an item waiting for the owner. Arrived orders items within a lane.
// Lease is the token Muster leased it under, or empty for an item added by
// hand.
type Held struct {
	Arrived uint64    `json:"arrived"`
	Lease   string    `json:"lease,omitempty"`
	Item    item.Item `json:"item"`
}

// Entry is one record in the spool or the log. Lease is the token the record
// is sent under.
type Entry struct {
	Seq         uint64        `json:"seq"`
	Status      string        `json:"status"`
	Lease       string        `json:"lease,omitempty"`
	Fingerprint string        `json:"fingerprint,omitempty"`
	Record      record.Record `json:"record"`
}

// Store is a directory of state.
type Store struct {
	root string
}

func unavailable() error { return rule.Fail(rule.StoreUnavailable, "") }

var dirs = []string{"pending", "outbox", "done"}

// Open prepares the directory. It writes nowhere else.
func Open(root string) (*Store, error) {
	s := &Store{root: root}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			return nil, unavailable()
		}
	}
	unlock, err := lock(filepath.Join(root, "lock"))
	if err != nil {
		return nil, unavailable()
	}
	defer unlock()
	// Remove what an interrupted write left behind.
	for _, d := range dirs {
		names, err := os.ReadDir(filepath.Join(root, d))
		if err != nil {
			return nil, unavailable()
		}
		for _, n := range names {
			if strings.HasPrefix(n.Name(), ".tmp-") {
				os.Remove(filepath.Join(root, d, n.Name()))
			}
		}
	}
	return s, nil
}

func (s *Store) locked(fn func() error) error {
	unlock, err := lock(filepath.Join(s.root, "lock"))
	if err != nil {
		return unavailable()
	}
	defer unlock()
	if err := fn(); err != nil {
		var pub *rule.Error
		if errors.As(err, &pub) {
			return pub
		}
		return unavailable()
	}
	return nil
}

func writeAtomic(dir, name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(append(b, '\n'))
	serr := f.Sync()
	cerr := f.Close()
	if err := errors.Join(werr, serr, cerr); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name)); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func removeSynced(dir, name string) error {
	if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDir(dir)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// names lists the regular entries of a subdirectory, sorted.
func (s *Store) names(sub string) ([]string, error) {
	ents, err := os.ReadDir(filepath.Join(s.root, sub))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		n := e.Name()
		if strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".json") {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func seqName(seq uint64) string { return fmt.Sprintf("%020d.json", seq) }

func seqOf(name string) (uint64, bool) {
	n, err := strconv.ParseUint(strings.TrimSuffix(name, ".json"), 10, 64)
	return n, err == nil
}

func (s *Store) pendingDir() string { return filepath.Join(s.root, "pending") }
func (s *Store) outboxDir() string  { return filepath.Join(s.root, "outbox") }
func (s *Store) doneDir() string    { return filepath.Join(s.root, "done") }

func (s *Store) readPending() ([]Held, error) {
	names, err := s.names("pending")
	if err != nil {
		return nil, err
	}
	out := make([]Held, 0, len(names))
	for _, n := range names {
		var h Held
		err := readJSON(filepath.Join(s.pendingDir(), n), &h)
		if errors.Is(err, fs.ErrNotExist) {
			continue // decided or dropped since the listing
		}
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := item.LaneRank(out[i].Item.Lane), item.LaneRank(out[j].Item.Lane)
		if ri != rj {
			return ri < rj
		}
		return out[i].Arrived < out[j].Arrived
	})
	return out, nil
}

func (s *Store) readEntries(sub string) ([]Entry, error) {
	names, err := s.names(sub)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(names))
	for _, n := range names {
		var e Entry
		err := readJSON(filepath.Join(s.root, sub, n), &e)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// nextSeq is one more than the highest sequence number in use. The newest
// done entry is never pruned, so numbers are not reused while history exists.
func (s *Store) nextSeq() (uint64, error) {
	var max uint64
	for _, sub := range []string{"outbox", "done"} {
		names, err := s.names(sub)
		if err != nil {
			return 0, err
		}
		for _, n := range names {
			if q, ok := seqOf(n); ok && q > max {
				max = q
			}
		}
	}
	return max + 1, nil
}

func (s *Store) spool(lease string, rec record.Record) error {
	seq, err := s.nextSeq()
	if err != nil {
		return err
	}
	return writeAtomic(s.outboxDir(), seqName(seq), Entry{Seq: seq, Status: Waiting, Lease: lease, Record: rec})
}

// Pending lists the items waiting for the owner: urgent lane first, then
// normal, then backfill, and in order of arrival within a lane.
func (s *Store) Pending() ([]Held, error) {
	out, err := s.readPending()
	if err != nil {
		return nil, unavailable()
	}
	return out, nil
}

// Get returns one waiting item.
func (s *Store) Get(id string) (Held, error) {
	if !rule.SafeID(id) {
		return Held{}, rule.Fail(rule.DecisionItemUnknown, "")
	}
	var h Held
	err := readJSON(filepath.Join(s.pendingDir(), id+".json"), &h)
	if errors.Is(err, fs.ErrNotExist) {
		return Held{}, rule.Fail(rule.DecisionItemUnknown, id)
	}
	if err != nil {
		return Held{}, unavailable()
	}
	return h, nil
}

// Accept puts a checked item in the inbox. lease is the token Muster leased
// it under, or empty for an item added by hand. An item that is already
// waiting keeps its place and takes the new lease.
func (s *Store) Accept(lease string, it item.Item) error {
	return s.locked(func() error {
		arrived := uint64(0)
		held, err := s.readPending()
		if err != nil {
			return err
		}
		for _, h := range held {
			if h.Item.ID == it.ID {
				arrived = h.Arrived
			}
		}
		if arrived == 0 {
			for _, h := range held {
				if h.Arrived > arrived {
					arrived = h.Arrived
				}
			}
			arrived++
		}
		return writeAtomic(s.pendingDir(), it.ID+".json", Held{Arrived: arrived, Lease: lease, Item: it})
	})
}

// Report spools what Countersign tells Muster about a leased item it will
// not show the owner: a fail for a broken item (E_PARSE), a needs_subtool
// for an unfamiliar one (E_NEEDS_SUBTOOL). The item leaves the inbox if it
// was there.
//
// If the same report about the same item is already waiting, or Muster has
// already refused it, nothing new is spooled and the result is nil. Muster
// offers an item again when its lease runs out, and saying the same thing
// each time would only fill the log.
func (s *Store) Report(lease string, perr *rule.Error, at time.Time) (*record.Record, error) {
	var out *record.Record
	err := s.locked(func() error {
		if perr.Item != "" {
			if err := removeSynced(s.pendingDir(), perr.Item+".json"); err != nil {
				return err
			}
		}
		call := record.Fail
		if perr.Code() == rule.NeedsSubtool {
			call = record.NeedsSubtool
		}
		for _, sub := range []string{"outbox", "done"} {
			entries, err := s.readEntries(sub)
			if err != nil {
				return err
			}
			for _, e := range entries {
				same := e.Record.Call == call && e.Record.Item == perr.Item && e.Record.Rule == string(perr.Rule)
				if same && perr.Item != "" && (e.Status == Waiting || e.Status == Refused) {
					return nil
				}
			}
		}
		rec := record.Record{
			Call:        call,
			Item:        perr.Item,
			At:          record.Stamp(at),
			Code:        string(perr.Code()),
			Rule:        string(perr.Rule),
			Fingerprint: perr.Fingerprint(),
		}
		if call == record.NeedsSubtool {
			rec.To = item.NeedsSubtool
		}
		rec = rec.Seal()
		if err := s.spool(lease, rec); err != nil {
			return err
		}
		out = &rec
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Decide records the owner's decision on a waiting item. The record goes to
// the spool first and the item leaves the inbox second, so a stop between
// the two cannot lose the decision. A refused decision changes nothing.
func (s *Store) Decide(d decide.Decision, at time.Time) (decide.Result, error) {
	var res decide.Result
	err := s.locked(func() error {
		if !rule.SafeID(d.Item) {
			return rule.Fail(rule.DecisionMalformed, "")
		}
		var h Held
		err := readJSON(filepath.Join(s.pendingDir(), d.Item+".json"), &h)
		if errors.Is(err, fs.ErrNotExist) {
			return rule.Fail(rule.DecisionItemUnknown, d.Item)
		}
		if err != nil {
			return err
		}
		waiting, err := s.readEntries("outbox")
		if err != nil {
			return err
		}
		for _, e := range waiting {
			if e.Record.Item == d.Item {
				// Already decided: the earlier stop came between the two steps.
				if err := removeSynced(s.pendingDir(), d.Item+".json"); err != nil {
					return err
				}
				return rule.Fail(rule.DecisionItemUnknown, d.Item)
			}
		}
		r, derr := decide.Apply(h.Item, d, at)
		if derr != nil {
			return derr
		}
		if err := s.spool(h.Lease, r.Record); err != nil {
			return err
		}
		if err := removeSynced(s.pendingDir(), d.Item+".json"); err != nil {
			return err
		}
		res = r
		return nil
	})
	if err != nil {
		return decide.Result{}, err
	}
	return res, nil
}

// Drop removes an item from the inbox without a decision, because its lease
// ended. Muster still has the item.
func (s *Store) Drop(id string) error {
	if !rule.SafeID(id) {
		return nil
	}
	return s.locked(func() error { return removeSynced(s.pendingDir(), id+".json") })
}

// Outbox lists the records Muster has not confirmed, oldest first.
func (s *Store) Outbox() ([]Entry, error) {
	out, err := s.readEntries("outbox")
	if err != nil {
		return nil, unavailable()
	}
	return out, nil
}

// Log lists recent records, oldest first: those confirmed or refused, then
// those still waiting.
func (s *Store) Log() ([]Entry, error) {
	done, err := s.readEntries("done")
	if err != nil {
		return nil, unavailable()
	}
	waiting, err := s.readEntries("outbox")
	if err != nil {
		return nil, unavailable()
	}
	all := append(done, waiting...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Seq < all[j].Seq })
	return all, nil
}

// Ack marks a spooled record as confirmed by Muster.
func (s *Store) Ack(seq uint64) error { return s.finish(seq, Acknowledged, "") }

// Refuse marks a spooled record as refused by Muster. It will not be retried.
func (s *Store) Refuse(seq uint64, fingerprint string) error {
	return s.finish(seq, Refused, fingerprint)
}

func (s *Store) finish(seq uint64, status, fingerprint string) error {
	return s.locked(func() error {
		var e Entry
		err := readJSON(filepath.Join(s.outboxDir(), seqName(seq)), &e)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // already finished
		}
		if err != nil {
			return err
		}
		e.Status, e.Fingerprint = status, fingerprint
		if err := writeAtomic(s.doneDir(), seqName(seq), e); err != nil {
			return err
		}
		if err := removeSynced(s.outboxDir(), seqName(seq)); err != nil {
			return err
		}
		names, err := s.names("done")
		if err != nil {
			return err
		}
		for i := 0; i < len(names)-KeepDone; i++ {
			if err := os.Remove(filepath.Join(s.doneDir(), names[i])); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}
