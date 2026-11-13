// Package muster is the small interface Countersign uses to reach Muster,
// with an in-memory stand-in. Muster itself is not built here.
package muster

import (
	"context"
	"encoding/json"

	"github.com/clearedstack/countersign/internal/fault"
)

// Client is what Countersign needs from Muster in this milestone: a lease of
// the items waiting for a person. Each item is handed over as raw JSON, so a
// broken item can be refused alone without losing the rest.
type Client interface {
	Lease(ctx context.Context) ([]json.RawMessage, error)
}

// Memory is the in-memory stand-in. It hands out each item once.
type Memory struct {
	waiting []json.RawMessage
}

// NewMemory builds a stand-in holding the given items.
func NewMemory(items []json.RawMessage) *Memory {
	return &Memory{waiting: append([]json.RawMessage(nil), items...)}
}

// FromJSON builds a stand-in from a document of the form {"items": [...]}.
func FromJSON(data []byte) (*Memory, error) {
	var doc struct {
		Items *[]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || doc.Items == nil {
		return nil, fault.New(fault.Parse, "lease-unreadable", "the leased data is not a list of items")
	}
	return NewMemory(*doc.Items), nil
}

// Lease returns every item not yet handed out, in order.
func (m *Memory) Lease(ctx context.Context) ([]json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, fault.New(fault.Timeout, "lease-timeout", "Muster took too long to answer a lease")
	}
	out := m.waiting
	m.waiting = nil
	return out, nil
}
