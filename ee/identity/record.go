// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import "sort"

// recordEventNameKey and recordSessionIDKey are envelope attributes on every log record,
// stripped from the payload before sanitization.
const (
	recordEventNameKey = "event.name"
	recordSessionIDKey = "session.id"
)

// unsetKeysAttributeKey is the record attribute carrying the key names of a $unset.
const unsetKeysAttributeKey = "keys"

// RequestFromRecord builds an identity Request from one decoded log record. The second
// return is false when the record carries nothing applicable (an empty $unset or $set).
// The caller's attributes map is never mutated; the payload is copied into a map this
// request owns.
func RequestFromRecord(appID string, easClientID string, op Op, attributes map[string]any, remoteIP string) (Request, bool) {
	req := Request{AppID: appID, EASClientID: easClientID, Op: op, RemoteIP: remoteIP}
	switch op {
	case OpUnset:
		rawKeys, _ := attributes[unsetKeysAttributeKey].([]any)
		for _, rawKey := range rawKeys {
			if key, ok := rawKey.(string); ok && key != "" {
				req.UnsetKeys = append(req.UnsetKeys, key)
			}
		}
		if len(req.UnsetKeys) == 0 {
			return Request{}, false
		}
	case OpSet, OpSetOnce:
		payload := make(map[string]any, len(attributes))
		for key, value := range attributes {
			if key == recordEventNameKey || key == recordSessionIDKey {
				continue
			}
			payload[key] = value
		}
		if len(payload) == 0 {
			return Request{}, false
		}
		req.Attributes = payload
	default:
		return Request{}, false
	}
	return req, true
}

// sameScalar reports whether two wire values are equal, without panicking on the
// uncomparable dynamic types (arrays, maps) the decoder can produce. Anything outside
// string/bool/int64/float64 is reported as different, which costs an extra group and
// never a wrong outcome.
func sameScalar(a, b any) bool {
	switch left := a.(type) {
	case string:
		right, ok := b.(string)
		return ok && left == right
	case bool:
		right, ok := b.(bool)
		return ok && left == right
	case int64:
		right, ok := b.(int64)
		return ok && left == right
	case float64:
		right, ok := b.(float64)
		return ok && left == right
	}
	return false
}

// CoalesceRequests reduces one installation's operations to the fewest store transactions
// that reach the same row. Requests naming more than one installation are handed back
// untouched.
//
// Two operations on the same key merge only when they are the same operation carrying the
// same value; a $set can be dropped downstream by Schema.Sanitize while a $unset never is,
// so a later write cannot be assumed to land, and any other merge risks losing data.
func CoalesceRequests(requests []Request) []Request {
	if len(requests) < 2 {
		return requests
	}
	for _, req := range requests[1:] {
		if req.AppID != requests[0].AppID || req.EASClientID != requests[0].EASClientID {
			return requests
		}
	}

	type write struct {
		op    Op
		value any
	}
	// A group is a set of operations on distinct keys that can share their transactions,
	// applied in order.
	type group struct {
		writes map[string]write
		// Key names in first-seen order, since ranging over the map is randomized.
		order []string
	}
	groups := []*group{{writes: map[string]write{}}}

	place := func(name string, op Op, value any) {
		current := groups[len(groups)-1]
		if held, taken := current.writes[name]; taken {
			// Same op, same value: costs no group regardless of what the schema decides.
			if held.op == op && sameScalar(held.value, value) {
				return
			}
			current = &group{writes: map[string]write{}}
			groups = append(groups, current)
		}
		current.writes[name] = write{op: op, value: value}
		current.order = append(current.order, name)
	}
	for _, req := range requests {
		switch req.Op {
		case OpUnset:
			for _, name := range req.UnsetKeys {
				place(name, OpUnset, nil)
			}
		case OpSet, OpSetOnce:
			// Sorted for a stable split; ranging over the map would vary run to run.
			names := make([]string, 0, len(req.Attributes))
			for name := range req.Attributes {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				place(name, req.Op, req.Attributes[name])
			}
		}
	}

	coalesced := make([]Request, 0, len(groups))
	for _, current := range groups {
		buckets := map[Op]Request{}
		for _, name := range current.order {
			decided := current.writes[name]
			bucket, open := buckets[decided.op]
			if !open {
				bucket = Request{
					AppID:       requests[0].AppID,
					EASClientID: requests[0].EASClientID,
					Op:          decided.op,
					RemoteIP:    requests[0].RemoteIP,
				}
				if decided.op != OpUnset {
					bucket.Attributes = map[string]any{}
				}
			}
			if decided.op == OpUnset {
				bucket.UnsetKeys = append(bucket.UnsetKeys, name)
			} else {
				bucket.Attributes[name] = decided.value
			}
			buckets[decided.op] = bucket
		}
		// Within a group the three ops touch disjoint keys, so this order is presentation only.
		for _, op := range []Op{OpSet, OpSetOnce, OpUnset} {
			if bucket, open := buckets[op]; open {
				coalesced = append(coalesced, bucket)
			}
		}
	}
	return coalesced
}
