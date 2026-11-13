// Package store keeps the held items and the recorded decisions in one JSON
// file. The file is replaced whole on every change, so a reader never sees a
// half-written store.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/clearedstack/countersign/internal/fault"
	"github.com/clearedstack/countersign/internal/queue"
)

// Version is the format of the store file.
const Version = 1

// ErrIO means the store file could not be read or written. The cause is
// deliberately dropped: it would name a path on this machine.
var ErrIO = errors.New("the store could not be read or written")

// Store is every item Countersign has held, in the order it received them,
// and every decision recorded, in the order they were made.
type Store struct {
	Version   int              `json:"version"`
	Items     []queue.Item     `json:"items"`
	Decisions []queue.Decision `json:"decisions"`
}

func empty() *Store {
	return &Store{Version: Version, Items: []queue.Item{}, Decisions: []queue.Decision{}}
}

func unreadable() error {
	return fault.New(fault.Parse, "store-unreadable", "the store is not in a form Countersign can read")
}

// Load reads the store. A file that does not exist is an empty store.
func Load(path string) (*Store, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return empty(), nil
	}
	if err != nil {
		return nil, ErrIO
	}
	s := &Store{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(s); err != nil || dec.More() {
		return nil, unreadable()
	}
	if s.Version != Version || s.Items == nil || s.Decisions == nil {
		return nil, unreadable()
	}
	// The store must agree with itself: ids are unique, and decisions are
	// numbered 1, 2, 3... with one decision at most per held item.
	ids := map[string]bool{}
	for _, it := range s.Items {
		if it.ID == "" || ids[it.ID] {
			return nil, unreadable()
		}
		ids[it.ID] = true
	}
	decided := map[string]bool{}
	for i, d := range s.Decisions {
		if d.Seq != i+1 || !ids[d.ItemID] || decided[d.ItemID] {
			return nil, unreadable()
		}
		decided[d.ItemID] = true
	}
	return s, nil
}

// Save replaces the store file.
func (s *Store) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return ErrIO
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".countersign-*")
	if err != nil {
		return ErrIO
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return ErrIO
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return ErrIO
	}
	if err := tmp.Close(); err != nil {
		return ErrIO
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return ErrIO
	}
	return nil
}

func (s *Store) index(id string) int {
	for i := range s.Items {
		if s.Items[i].ID == id {
			return i
		}
	}
	return -1
}

// Has reports whether an item with this id was ever held.
func (s *Store) Has(id string) bool { return s.index(id) >= 0 }

// Hold adds an admitted item to the store.
func (s *Store) Hold(it queue.Item) { s.Items = append(s.Items, it) }

func (s *Store) decided(id string) bool {
	for _, d := range s.Decisions {
		if d.ItemID == id {
			return true
		}
	}
	return false
}

// Pending lists the items no decision has been recorded for, oldest first.
func (s *Store) Pending() []queue.Item {
	out := []queue.Item{}
	for _, it := range s.Items {
		if !s.decided(it.ID) {
			out = append(out, it)
		}
	}
	return out
}

// Decide records a person's outcome for a pending item. When the outcome is
// refused, the store is left exactly as it was.
func (s *Store) Decide(id, outcome, reason string) (queue.Decision, *fault.Error) {
	i := s.index(id)
	if i < 0 || s.decided(id) {
		return queue.Decision{}, fault.New(fault.Parse, "item-not-pending", "no pending item has that id")
	}
	d, ferr := queue.Decide(s.Items[i], outcome, reason)
	if ferr != nil {
		return queue.Decision{}, ferr
	}
	d.Seq = len(s.Decisions) + 1
	s.Decisions = append(s.Decisions, d)
	s.Items[i].State = d.To
	return d, nil
}
