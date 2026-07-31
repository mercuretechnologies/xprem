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
		})
		require.True(t, ok)
		require.Equal(t, OpSet, req.Op)
		require.Equal(t, map[string]any{"userId": "u1"}, req.Attributes)
	})

	t.Run("set with only envelope is skipped", func(t *testing.T) {
		_, ok := RequestFromRecord("app", "client", OpSet, map[string]any{
			"event.name": "$set",
			"session.id": "aaaa",
		})
		require.False(t, ok)
	})

	t.Run("unset reads its keys array and tolerates junk entries", func(t *testing.T) {
		req, ok := RequestFromRecord("app", "client", OpUnset, map[string]any{
			"event.name": "$unset",
			"keys":       []any{"userId", "", 42, "tenant"},
		})
		require.True(t, ok)
		require.Equal(t, []string{"userId", "tenant"}, req.UnsetKeys)
	})

	t.Run("unset without usable keys is skipped", func(t *testing.T) {
		_, ok := RequestFromRecord("app", "client", OpUnset, map[string]any{"event.name": "$unset"})
		require.False(t, ok)
		_, ok = RequestFromRecord("app", "client", OpUnset, map[string]any{"keys": "userId"})
		require.False(t, ok)
	})

	t.Run("unknown op is skipped", func(t *testing.T) {
		_, ok := RequestFromRecord("app", "client", Op("identify"), map[string]any{"userId": "u1"})
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
		// If "2" is dropped by the schema, "1" must still have landed.
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
		// "organic" cannot be assumed to land, so "paid" must not be folded into it.
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
		require.Len(t, out, 1)
		require.Equal(t, map[string]any{"userId": "u42"}, out[0].Attributes)
	})

	t.Run("a delete is never swallowed by a later write", func(t *testing.T) {
		out := CoalesceRequests([]Request{
			{AppID: "app", EASClientID: "d1", Op: OpUnset, UnsetKeys: []string{"plan"}},
			set("d1", map[string]any{"plan": 42}),
		})
		// The 42 can be dropped for its type; the deletion must still survive.
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

// applySequentially is a naive replay of the requests, the reference CoalesceRequests's
// output must match. rejected names keys the store would drop, since $set/$set_once are
// filtered per key while $unset never is.
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
			// Never filtered, so it still works for keys the allowlist has since dropped.
			for _, key := range req.UnsetKeys {
				delete(result, key)
			}
		}
	}
	return result
}

// TestCoalesceRequestsPreservesTheOutcome fuzzes random operation sequences and checks
// that CoalesceRequests's output reaches the same row as a naive replay, with one key
// always rejected to exercise "a write may be dropped, a delete always lands".
func TestCoalesceRequestsPreservesTheOutcome(t *testing.T) {
	random := rand.New(rand.NewSource(20260727))
	keys := []string{"a", "b", "c"}
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

		// The reference replay runs on a copy taken before the fold touches anything.
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
		// Same input must produce the same split every time.
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
		require.Equal(t,
			applySequentially(initial, reference, nil),
			applySequentially(initial, folded, nil),
			"attempt %d with everything accepted: %v", attempt, reference)
	}
}

// Array and map attribute values are legal but uncomparable with `==`, which used to panic.
func TestCoalesceRequestsSurvivesUncomparableValues(t *testing.T) {
	for _, value := range []any{
		[]any{"a", "b"},
		map[string]any{"nested": "v"},
		[]any{map[string]any{"deep": []any{1}}},
	} {
		require.NotPanics(t, func() {
			out := CoalesceRequests([]Request{
				{AppID: "app", EASClientID: "d1", Op: OpSet, Attributes: map[string]any{"k": value}},
				{AppID: "app", EASClientID: "d1", Op: OpSet, Attributes: map[string]any{"k": value}},
			})
			// Reported as different rather than merged; the store would drop such a value anyway.
			require.Len(t, out, 2)
		}, "%T", value)
	}

	// The scalars the store can actually keep still merge, including the two
	// numeric shapes the decoder produces.
	for _, value := range []any{"v", true, int64(42), 4.2} {
		out := CoalesceRequests([]Request{
			{AppID: "app", EASClientID: "d1", Op: OpSet, Attributes: map[string]any{"k": value}},
			{AppID: "app", EASClientID: "d1", Op: OpSet, Attributes: map[string]any{"k": value}},
		})
		require.Len(t, out, 1, "%T", value)
	}

	// Same type, different value: not the same write.
	out := CoalesceRequests([]Request{
		{AppID: "app", EASClientID: "d1", Op: OpSet, Attributes: map[string]any{"k": "a"}},
		{AppID: "app", EASClientID: "d1", Op: OpSet, Attributes: map[string]any{"k": "b"}},
	})
	require.Len(t, out, 2)
}
