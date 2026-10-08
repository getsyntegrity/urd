// MIT License
//
// Copyright (c) 2022-2026 Arsene Tochemey Gandote
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package readertck

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"

	"github.com/getsyntegrity/urd/egopb"
	"github.com/getsyntegrity/urd/persistence"
)

// This file holds the two subjects that every backend of the harness reuses, so
// the in-memory run (testkit) and the PostgreSQL run of the integration lane
// evaluate exactly the same code. Both are written only against
// persistence.EventsStore. Neither recommends a read mechanism.

// Admit applies the selection checks of the harness to a request: a selection
// built on the invalid zero Scope, AllScopesInCell without the privilege, and a
// request without a selection are refused. The current read API has no notion
// of selection, so LegacyReader adds this and nothing else.
func Admit(req Request) error {
	switch {
	case req.Selection.Kind == KindOneScope && !req.Selection.Scope.Valid():
		return fmt.Errorf("zero scope: %w", ErrRejected)
	case req.Selection.Kind == KindAllScopesInCell && !req.Privileged:
		return fmt.Errorf("all scopes without privilege: %w", ErrRejected)
	case req.Selection.Kind != KindOneScope && req.Selection.Kind != KindAllScopesInCell:
		return fmt.Errorf("no selection: %w", ErrRejected)
	}
	return nil
}

// EventFromProto is the harness's view of a stored event. The slice of the
// harness travels in Event.Shard, as the scenarios write it.
func EventFromProto(scope persistence.Scope, pb *egopb.Event) Event {
	return Event{
		Label:         pb.GetPersistenceId(),
		Scope:         scope,
		PersistenceID: pb.GetPersistenceId(),
		Seq:           pb.GetSequenceNumber(),
		Slice:         uint32(pb.GetShard()),
		Timestamp:     pb.GetTimestamp(),
	}
}

func encodeCursor(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return out
}

// LegacyReader is the CURRENT read API, GetShardEvents(scope, shard, offset,
// limit), behind the provisional Subject. Its cursor is the per-(scope, shard)
// timestamp offsets. The shim adds the selection checks (Admit) the old API has
// no notion of, nothing else. Scopes and Slices are the cell's scopes and slices
// as the scenario declares them, because EventsStore cannot enumerate scopes.
// It is expected to fail the scenarios in which an event commits behind the
// cursor (the per-property baseline is legacyKnownFailures in testkit_test.go).
type LegacyReader struct {
	Store  persistence.EventsStore
	Scopes []persistence.Scope
	Slices []uint32
}

// Poll implements Subject.
func (r LegacyReader) Poll(req Request) (Result, error) {
	if err := Admit(req); err != nil {
		return Result{}, err
	}
	offsets := map[string]int64{}
	if len(req.Cursor) > 0 {
		if err := json.Unmarshal(req.Cursor, &offsets); err != nil {
			return Result{}, err
		}
	}
	var out []Delivery
	for i, scope := range r.Scopes {
		if !req.Selection.Matches(scope) {
			continue
		}
		for _, slice := range r.Slices {
			if !req.Slices.Contains(slice) {
				continue
			}
			key := fmt.Sprintf("%d/%d", i, slice)
			evs, next, err := r.Store.GetShardEvents(context.Background(), scope, uint64(slice), offsets[key], uint64(req.Limit))
			if err != nil {
				return Result{}, err
			}
			for _, pb := range evs {
				e := EventFromProto(scope, pb)
				out = append(out, Delivery{Event: e, Token: e.Key().String()})
			}
			if len(evs) > 0 {
				offsets[key] = next
			}
		}
	}
	return Result{Deliveries: out, Cursor: encodeCursor(offsets)}, nil
}

// StoreSetReader is a correct reference reader written only against the
// EventsStore interface: it enumerates every persistence id of every scope of
// the cell, replays them, and remembers the delivered keys in the cursor.
// O(journal) per poll, test-only, no mechanism recommended. The list of scopes
// has to come from the harness because EventsStore cannot enumerate scopes: a
// capability gap the SPI will have to cover (see the document).
type StoreSetReader struct {
	Store  persistence.EventsStore
	Scopes []persistence.Scope
}

// Poll implements Subject.
func (r StoreSetReader) Poll(req Request) (Result, error) {
	if err := Admit(req); err != nil {
		return Result{}, err
	}
	var seen struct{ D []string }
	if len(req.Cursor) > 0 {
		if err := json.Unmarshal(req.Cursor, &seen); err != nil {
			return Result{}, err
		}
	}
	type cand struct {
		ev  Event
		key string
	}
	var cands []cand
	ctx := context.Background()
	for i, scope := range r.Scopes {
		if !req.Selection.Matches(scope) {
			continue
		}
		token := ""
		for {
			ids, next, err := r.Store.PersistenceIDs(ctx, scope, 100, token)
			if err != nil {
				return Result{}, err
			}
			for _, id := range ids {
				evs, err := r.Store.ReplayEvents(ctx, scope, id, 0, math.MaxUint64, 1<<20)
				if err != nil {
					return Result{}, err
				}
				for _, pb := range evs {
					e := EventFromProto(scope, pb)
					key := fmt.Sprintf("%d|%s|%d", i, e.PersistenceID, e.Seq)
					if req.Slices.Contains(e.Slice) && !slices.Contains(seen.D, key) {
						cands = append(cands, cand{e, key})
					}
				}
			}
			if next == "" {
				break
			}
			token = next
		}
	}
	slices.SortFunc(cands, func(a, b cand) int {
		return cmp.Or(cmp.Compare(a.ev.Timestamp, b.ev.Timestamp), cmp.Compare(a.ev.PersistenceID, b.ev.PersistenceID), cmp.Compare(a.ev.Seq, b.ev.Seq))
	})
	if len(cands) > req.Limit {
		cands = cands[:req.Limit]
	}
	var out []Delivery
	for _, c := range cands {
		out = append(out, Delivery{Event: c.ev, Token: c.key})
		seen.D = append(seen.D, c.key)
	}
	return Result{Deliveries: out, Cursor: encodeCursor(seen)}, nil
}
