// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import (
	"math/rand"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestFromRecord(t *testing.T) {
	t.Run("set strips the envelope and keeps the payload", func(t *testing.T) {
		req, ok := RequestFromRecord("app", "client", OpSet, map[string]any{
			"event.name": "$set",
			"session.id": "aaaa",
			"userId":     "u1",
		}, "203.0.113.7")
		require.True(t, ok)
		require.Equal(t, OpSet, req.Op)
		require.Equal(t, map[string]any{"userId": "u1"}, req.Attributes)
		require.Equal(t, "203.0.113.7", req.RemoteIP)
	})

	t.Run("set with only envelope is skipped", func(t *testing.T) {
		_, ok := RequestFromRecord("app", "client", OpSet, map[string]any{
			"event.name": "$set",
			"session.id": "aaaa",
		}, "")
		require.False(t, ok)
	})

	t.Run("unset reads its keys array and tolerates junk entries", func(t *testing.T) {
		req, ok := RequestFromRecord("app", "client", OpUnset, map[string]any{
			"event.name": "$unset",
			"keys":       []any{"userId", "", 42, "tenant"},
		}, "")
		require.True(t, ok)
		require.Equal(t, []string{"userId", "tenant"}, req.UnsetKeys)
	})

	t.Run("unset without usable keys is skipped", func(t *testing.T) {
		_, ok := RequestFromRecord("app", "client", OpUnset, map[string]any{"event.name": "$unset"}, "")
		require.False(t, ok)
		_, ok = RequestFromRecord("app", "client", OpUnset, map[string]any{"keys": "userId"}, "")
		require.False(t, ok)
	})

	t.Run("unknown op is skipped", func(t *testing.T) {
		_, ok := RequestFromRecord("app", "client", Op("identify"), map[string]any{"userId": "u1"}, "")
		require.False(t, ok)
	})
}

func TestCoalesceRequests(t *testing.T) {
	set := func(device string, kv map[string]any) Request {
		return Request{AppID: "app", EASClientID: device, Op: OpSet, Attributes: kv}
	}

	t.Run("a key that changes value opens a second transaction", func(t *testing.T) {
		out := CoalesceRequests([]Request{
			set("d1", map[string]any{"a": "1", "b": "1"}),
			set("d1", map[string]any{"b": "2"}),
		})
		// b changes, so it cannot share a transaction with its own earlier
		// value: if "2" is dropped by the schema, "1" has to have landed. a
		// never repeats, so it stays in the first.
		require.Len(t, out, 2)
		require.Equal(t, map[string]any{"a": "1", "b": "1"}, out[0].Attributes)
		require.Equal(t, map[string]any{"b": "2"}, out[1].Attributes)
	})

	t.Run("keys that never repeat all share one transaction", func(t *testing.T) {
		out := CoalesceRequests([]Request{
			set("d1", map[string]any{"a": "1"}),
			set("d1", map[string]any{"b": "2"}),
			set("d1", map[string]any{"c": "3"}),
		})
		require.Len(t, out, 1)
		require.Equal(t, map[string]any{"a": "1", "b": "2", "c": "3"}, out[0].Attributes)
	})

	t.Run("two values for one key cost a second transaction", func(t *testing.T) {
		out := CoalesceRequests([]Request{
			{AppID: "app", EASClientID: "d1", Op: OpSetOnce, Attributes: map[string]any{"ref": "organic"}},
			{AppID: "app", EASClientID: "d1", Op: OpSetOnce, Attributes: map[string]any{"ref": "paid", "v": "1"}},
		})
		// "organic" cannot be assumed to land: an undeclared key, a bad type or
		// a lapsed licence drops it, and then "paid" is the one that decides.
		// The key that repeats opens a group; "v" rides along in it.
		require.Len(t, out, 2)
		require.Equal(t, map[string]any{"ref": "organic"}, out[0].Attributes)
		require.Equal(t, map[string]any{"ref": "paid", "v": "1"}, out[1].Attributes)
	})

	t.Run("the same value twice costs nothing", func(t *testing.T) {
		out := CoalesceRequests([]Request{
			set("d1", map[string]any{"userId": "u42"}),
			set("d1", map[string]any{"userId": "u42"}),
			set("d1", map[string]any{"userId": "u42"}),
		})
		// What a real backlog is made of: the same identity re-sent every
		// session. Whatever the schema decides, it decides it once.
		require.Len(t, out, 1)
		require.Equal(t, map[string]any{"userId": "u42"}, out[0].Attributes)
	})

	t.Run("a delete is never swallowed by a later write", func(t *testing.T) {
		out := CoalesceRequests([]Request{
			{AppID: "app", EASClientID: "d1", Op: OpUnset, UnsetKeys: []string{"plan"}},
			set("d1", map[string]any{"plan": 42}),
		})
		// The 42 is dropped for its type while the delete is not filtered at
		// all, so folding to the write alone would leave the old value in
		// place forever. The deletion the device asked for must survive.
		require.Len(t, out, 2)
		require.Equal(t, OpUnset, out[0].Op)
		require.Equal(t, []string{"plan"}, out[0].UnsetKeys)
		require.Equal(t, OpSet, out[1].Op)
	})

	t.Run("adjacent unsets append keys", func(t *testing.T) {
		out := CoalesceRequests([]Request{
			{AppID: "app", EASClientID: "d1", Op: OpUnset, UnsetKeys: []string{"a"}},
			{AppID: "app", EASClientID: "d1", Op: OpUnset, UnsetKeys: []string{"b"}},
		})
		require.Len(t, out, 1)
		require.Equal(t, []string{"a", "b"}, out[0].UnsetKeys)
	})

	// A batch names one installation: the app comes from the URL and the client
	// id is persisted per install, and the ingest handler caps it before this
	// runs. Folding two of them into one row would be silent corruption, so the
	// input comes back untouched instead.
	t.Run("more than one device is handed back untouched", func(t *testing.T) {
		in := []Request{
			set("d1", map[string]any{"a": "1"}),
			set("d2", map[string]any{"a": "x"}),
			set("d1", map[string]any{"b": "2"}),
		}
		require.Equal(t, in, CoalesceRequests(in))
	})

	t.Run("same client id in different apps does not fold", func(t *testing.T) {
		in := []Request{
			set("d1", map[string]any{"a": "1"}),
			{AppID: "other-app", EASClientID: "d1", Op: OpSet, Attributes: map[string]any{"b": "2"}},
		}
		require.Equal(t, in, CoalesceRequests(in))
	})
}

// applySequentially is what the store does, spelled out: the reference the fold
// has to match. Deliberately a naive replay, because a fold that is only
// checked against itself proves nothing.
//
// `rejected` is the piece whose absence hid a real bug: the store filters a
// $set and a $set_once per key (Schema.Sanitize drops an undeclared key or a
// value of the wrong type, and a lapsed licence empties them entirely) while it
// filters a $unset by neither, on purpose. A reference that applies everything
// models a store that does not exist, and no number of iterations can then
// catch a fold that assumes its writes land.
func applySequentially(state map[string]any, requests []Request, rejected map[string]bool) map[string]any {
	result := map[string]any{}
	for key, value := range state {
		result[key] = value
	}
	for _, req := range requests {
		switch req.Op {
		case OpSet:
			for key, value := range req.Attributes {
				if rejected[key] {
					continue
				}
				result[key] = value
			}
		case OpSetOnce:
			for key, value := range req.Attributes {
				if rejected[key] {
					continue
				}
				if _, held := result[key]; !held {
					result[key] = value
				}
			}
		case OpUnset:
			// Never filtered: it is the cleanup path, and it works for keys the
			// allowlist has since dropped.
			for _, key := range req.UnsetKeys {
				delete(result, key)
			}
		}
	}
	return result
}

// The fold rewrites a sequence of operations into a shorter one, so the only
// property that matters is that both reach the same row. Three keys and three
// operations over a random starting row cover the orderings that make this
// non-obvious far better than cases picked by hand, and one key is always
// refused by the schema so the asymmetry between "a write may be dropped" and
// "a delete always lands" is exercised on every attempt.
func TestCoalesceRequestsPreservesTheOutcome(t *testing.T) {
	random := rand.New(rand.NewSource(20260727))
	keys := []string{"a", "b", "c"}
	// "c" stands for every reason a write does not land: undeclared, wrong
	// type, too long, or no licence.
	rejected := map[string]bool{"c": true}

	for attempt := 0; attempt < 5000; attempt++ {
		initial := map[string]any{}
		for _, key := range keys {
			if random.Intn(2) == 0 {
				initial[key] = "was-" + key
			}
		}

		requests := make([]Request, 0, 6)
		for i := 0; i < 1+random.Intn(6); i++ {
			key := keys[random.Intn(len(keys))]
			req := Request{AppID: "app", EASClientID: "d1"}
			switch random.Intn(3) {
			case 0:
				req.Op, req.Attributes = OpSet, map[string]any{key: "set" + strconv.Itoa(i)}
			case 1:
				req.Op, req.Attributes = OpSetOnce, map[string]any{key: "once" + strconv.Itoa(i)}
			default:
				req.Op, req.UnsetKeys = OpUnset, []string{key}
			}
			requests = append(requests, req)
		}

		// CoalesceRequests reads the requests it is given, so the reference
		// replay runs on a copy taken before the fold touches anything.
		reference := make([]Request, len(requests))
		for i, req := range requests {
			reference[i] = Request{AppID: req.AppID, EASClientID: req.EASClientID, Op: req.Op, UnsetKeys: req.UnsetKeys}
			if req.Attributes != nil {
				reference[i].Attributes = map[string]any{}
				for key, value := range req.Attributes {
					reference[i].Attributes[key] = value
				}
			}
		}

		folded := CoalesceRequests(requests)
		// Same input, same split: which key opens a group boundary decides
		// where the others land, so ranging over an attribute map would make
		// one batch produce different shapes from one run to the next.
		if attempt%50 == 0 {
			again := make([]Request, len(reference))
			copy(again, reference)
			require.Equal(t, folded, CoalesceRequests(again), "attempt %d is not deterministic", attempt)
		}
		require.LessOrEqual(t, len(folded), len(reference),
			"the fold may never cost more transactions than the replay it replaces")
		require.Equal(t,
			applySequentially(initial, reference, rejected),
			applySequentially(initial, folded, rejected),
			"attempt %d: %v", attempt, reference)
		// And the same holds when nothing is refused, which is the ordinary
		// case: a fold that only works under one schema is not a fold.
		require.Equal(t,
			applySequentially(initial, reference, nil),
			applySequentially(initial, folded, nil),
			"attempt %d with everything accepted: %v", attempt, reference)
	}
}
