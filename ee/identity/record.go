// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

import "sort"

// Identity consumes log records already decoded by the transport (ee/observe
// owns the OTLP wire format); this file owns the identity-side conventions:
// which record attributes form the operation payload.

// The two record attributes the client SDK reserves on every log record; they
// are envelope, not payload, and are stripped before sanitization. Stripping
// is identity policy and lives here on purpose: keyPattern allows dots, so an
// operator could declare a metadata key literally named "event.name", and the
// future ClickHouse flattener will keep these as real columns rather than
// strip them. recordEventNameKey mirrors observe.EventNameKey by necessity
// (the observe → identity dependency forbids importing it back).
const (
	recordEventNameKey = "event.name"
	recordSessionIDKey = "session.id"
)

// unsetKeysAttributeKey is the record attribute carrying the key names of a
// $unset: `logEvent('$unset', { attributes: { keys: ['userId'] } })`. An
// explicit array attribute because null values never leave the stock SDK
// (dropped client-side), so "set to null" cannot express removal.
const unsetKeysAttributeKey = "keys"

// RequestFromRecord builds an identity Request from one decoded log record.
// The second return is false when the record carries nothing applicable
// (a $unset without keys, a $set with an empty payload): skipping those saves
// a store transaction that would be a no-op. The caller's map is never
// mutated: the decoded attributes are the same map the telemetry pass reads
// afterwards to recognize an identity record, and stripping the envelope in
// place made that record unrecognizable, so its payload was persisted as a
// nameless log line. The payload is copied into a map this request owns,
// which CoalesceRequests is then free to merge into.
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

// CoalesceRequests reduces ONE installation's operations to the fewest store
// transactions that reach the SAME row, so a backlog does not cost one
// transaction per operation. The SDK ships its whole backlog in a single POST,
// and a device that wrote the same attribute forty times is describing one
// outcome.
//
// One installation, because that is all a batch can name: the app comes from
// the URL and the client id is persisted per install, so the caller refuses a
// body naming two before anything reaches here. A request naming another one is
// therefore not something to merge, and the whole input is handed back
// untouched rather than folded into a row it does not belong to.
//
// THE RULE: two operations on the same key merge only when they are the same
// operation carrying the same value. Anything else opens a group boundary, and
// operations on other keys keep merging into the current group.
//
// That rule is stricter than it looks like it needs to be, and the reason is an
// asymmetry in the store rather than anything about ordering. A $set and a
// $set_once are filtered per key: an undeclared key, a value of the wrong type
// or one past its length is dropped by Schema.Sanitize, and without a licence
// they carry nothing at all. A $unset is filtered by neither, deliberately, so
// that removing data keeps working when collecting it no longer does. So a
// later write CANNOT be assumed to land, and every shortcut that assumes it
// does is wrong:
//
//	$unset{plan}, $set{plan:42}   the 42 is dropped for its type, and folding
//	                             to the $set alone loses the deletion, leaving
//	                             the old value in place forever
//	$set{ref:42}, $set_once{ref}  the 42 is dropped, so the $set_once is the
//	                             one that lands, and dropping it as "already
//	                             present" loses the value
//	$set{k:"ok"}, $set{k:42}      the 42 is dropped, so a replay leaves "ok",
//	                             while folding to the last write leaves
//	                             whatever the row held before the batch
//
// Merging equal writes is what stays safe, and it is also what a real backlog
// is made of: a device that re-sends the same userId every session collapses to
// one transaction. A key whose value genuinely changes costs one more group,
// which is the honest price of not guessing what the schema will accept.
func CoalesceRequests(requests []Request) []Request {
	if len(requests) < 2 {
		return requests
	}
	for _, req := range requests[1:] {
		if req.AppID != requests[0].AppID || req.EASClientID != requests[0].EASClientID {
			return requests
		}
	}

	// What one key is doing in one group.
	type write struct {
		op    Op
		value any
	}
	// A group is a set of operations on distinct keys that can share their
	// transactions. Groups are applied in order, so a key appearing in two of
	// them is written twice, in the order the device sent it.
	type group struct {
		writes map[string]write
		// Key names in the order they were first seen: ranging over a Go map is
		// randomized, and the output has to be stable.
		order []string
	}
	groups := []*group{{writes: map[string]write{}}}

	place := func(name string, op Op, value any) {
		current := groups[len(groups)-1]
		if held, taken := current.writes[name]; taken {
			// Same operation, same value: the second one changes nothing
			// whatever the schema decides, so it costs no group.
			if held.op == op && held.value == value {
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
			// Sorted, because which key is placed first decides which group the
			// others land in when one of them opens a boundary. Ranging over
			// the map would make the same batch produce different (equivalent)
			// splits from one run to the next.
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
		// Within a group the three touch disjoint keys, so their order is
		// presentation; across groups it is semantics, and the loop above
		// already walks them in order.
		for _, op := range []Op{OpSet, OpSetOnce, OpUnset} {
			if bucket, open := buckets[op]; open {
				coalesced = append(coalesced, bucket)
			}
		}
	}
	return coalesced
}
