// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package identity

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

// CoalesceRequests folds ADJACENT same-op requests of the same installation
// into one store transaction: the SDK ships its whole backlog in a single
// batch, so several $set operations commonly arrive together. Only adjacent
// operations from the same app and device merge; folding across another
// request would reorder the event timeline.
func CoalesceRequests(requests []Request) []Request {
	if len(requests) < 2 {
		return requests
	}
	coalesced := make([]Request, 0, len(requests))
	for _, req := range requests {
		if len(coalesced) > 0 {
			previous := &coalesced[len(coalesced)-1]
			if previous.AppID != req.AppID || previous.EASClientID != req.EASClientID || previous.Op != req.Op {
				coalesced = append(coalesced, req)
				continue
			}
			switch req.Op {
			case OpUnset:
				previous.UnsetKeys = append(previous.UnsetKeys, req.UnsetKeys...)
				continue
			case OpSet:
				for key, value := range req.Attributes {
					previous.Attributes[key] = value
				}
				continue
			case OpSetOnce:
				// First value wins within the fold, matching the store.
				for key, value := range req.Attributes {
					if _, held := previous.Attributes[key]; !held {
						previous.Attributes[key] = value
					}
				}
				continue
			}
		}
		coalesced = append(coalesced, req)
	}
	return coalesced
}
