// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"expo-open-ota/ee/identity"
	"expo-open-ota/internal/cache"
	"expo-open-ota/internal/handlers"
	"hash/fnv"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CheckInRecorder registers device check-ins in the universal registry
// (device_identity): every manifest poll and every telemetry batch lands
// there, identity ops only layer metadata on top. Besides existence, a
// check-in carries update-health state: the update the device currently runs
// and the updates that crashed on it at launch (manifest error-recovery
// headers), persisted in Postgres so instant-T adoption and health need no
// ClickHouse and no SDK.
//
// The registry is uncapped; what the recorder provides is the WRITE-RATE
// bound: the cached value holds the last recorded state, so a steady-state
// device costs one registry write per TTL while a real state change (new
// current update, new failure set, a fatal error) is recorded immediately.
type CheckInRecorder struct {
	identity *identity.Service
	cache    cache.Cache
}

func NewCheckInRecorder(identityService *identity.Service, c cache.Cache) *CheckInRecorder {
	return &CheckInRecorder{identity: identityService, cache: c}
}

// checkInTTLSeconds bounds the steady-state last_seen bump rate per device;
// ±60s of last_seen precision is far inside what any consumer needs.
const checkInTTLSeconds = 60

// checkInErrorCacheValue marks a failed registration: retry after the TTL
// instead of one doomed transaction and one log line per poll (database
// down, app row deleted under a live fleet).
const checkInErrorCacheValue = "e"

// fatalStashTTLSeconds keeps an unsaved fatal error around long enough to
// survive a registry outage. The client sends the crash detail exactly once;
// if the write that carried it failed, the stash re-attaches it to the next
// successful failure write (the failed-ids header is sticky, the detail is
// not).
const fatalStashTTLSeconds = 3600

func checkInCacheKey(appID, easClientID string) string {
	return "observe:checkin:" + appID + ":" + easClientID
}

func fatalStashKey(appID, easClientID string) string {
	return "observe:fatal:" + appID + ":" + easClientID
}

// maxFatalErrorRunes bounds the crash detail before it is stored anywhere. It
// arrives either as an OTLP attribute of a batch up to 16MB or as a manifest
// header bounded only by the server's header limit, and from there it is copied
// into PostgreSQL, into the outbox, into ClickHouse and into the cache stash,
// so an unbounded value is amplified four times over. The client's own contract
// truncates a log body at 4096 characters, and a crash message that needs more
// than that is a stack trace nobody reads from a table cell.
const maxFatalErrorRunes = 4096

// boundFatalError caps the crash detail at maxFatalErrorRunes, counted in runes
// so a multi-byte message is never cut mid-character.
func boundFatalError(detail string) string {
	if runes := []rune(detail); len(runes) > maxFatalErrorRunes {
		return string(runes[:maxFatalErrorRunes])
	}
	return detail
}

// checkInState is a check-in reduced to its EFFECTIVE update-health state,
// normalized so that every source describes the same device state with the
// same strings. The wire is inconsistent on purpose-defeating details: the
// manifest header carries raw (possibly uppercase) UUIDs while telemetry
// carries normalized ones, telemetry uses the zero-UUID sentinel where the
// manifest omits the header, and telemetry never knows the failure list.
// Fingerprinting raw check-ins would bust the debounce on every
// manifest/telemetry alternation; normalizing first is what makes the
// write-rate bound real for dual-source devices.
type checkInState struct {
	// currentUpdateID is the canonical lowercase uuid of the running update,
	// "" when this check-in does not know it (embedded-bundle telemetry, no
	// header). "" never overwrites a known value downstream.
	currentUpdateID string
	// observedAt is when the device was running that update. A manifest poll
	// observes it as it answers; a telemetry batch observed it whenever its
	// newest record was written, which can be well before it arrives. The
	// store refuses an observation older than the one it holds, so this is
	// what stops a backlog flushed after an update from putting the registry
	// back on the update the device already left.
	observedAt time.Time
	// failedUpdateIDs is the parsed, normalized, sorted failure list; empty
	// when the check-in carries none (which, for telemetry, means "does not
	// know", not "no failures").
	failedUpdateIDs []string
	fatalError      string
	// device is the reported hardware and OS, zero when this check-in does
	// not know it (every manifest poll). Like the other components, unknown
	// never overwrites and never busts the debounce.
	device identity.DeviceInfo
}

func normalizeCheckIn(checkIn handlers.DeviceCheckIn, now time.Time) checkInState {
	state := checkInState{fatalError: boundFatalError(checkIn.FatalError), observedAt: checkIn.ObservedAt}
	// Never in the future. Telemetry timestamps are client-supplied and only
	// clamped to a day of skew, and one device with a fast clock would
	// otherwise record an observation no later check-in could beat, freezing
	// its own registry entry until real time caught up.
	if state.observedAt.IsZero() || state.observedAt.After(now) {
		state.observedAt = now
	}
	if parsed, err := uuid.Parse(checkIn.CurrentUpdateID); err == nil {
		if normalized := parsed.String(); normalized != ZeroUpdateID {
			state.currentUpdateID = normalized
		}
	}
	state.failedUpdateIDs = ParseFailedUpdateIDs(checkIn.FailedUpdateIDsRaw)
	sort.Strings(state.failedUpdateIDs)
	state.device = identity.DeviceInfo{
		Model:      strings.TrimSpace(checkIn.DeviceModel),
		OSName:     strings.TrimSpace(checkIn.OSName),
		OSVersion:  strings.TrimSpace(checkIn.OSVersion),
		AppVersion: strings.TrimSpace(checkIn.AppVersion),
	}
	return state
}

// deviceFingerprint condenses the reported hardware and store version, so a
// device that upgrades its OS or takes a store release is written through
// instead of waiting for an unrelated state change.
// Zero info fingerprints to "", which reads as "unknown" everywhere below.
func deviceFingerprint(device identity.DeviceInfo) string {
	if device.IsZero() {
		return ""
	}
	h := fnv.New64a()
	for _, part := range []string{device.Model, device.OSName, device.OSVersion, device.AppVersion} {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return "d" + strconv.FormatUint(h.Sum64(), 36)
}

// failedFingerprint condenses the normalized failure list; FNV-1a like the
// telemetry content hash. The "f" prefix keeps every real cache value out of
// the error sentinel's ("e") value space.
func failedFingerprint(ids []string) string {
	h := fnv.New64a()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
	}
	return "f" + strconv.FormatUint(h.Sum64(), 36)
}

// cachedCheckInValue encodes the last recorded state: current uuid (may be
// empty), failure fingerprint and hardware fingerprint, parseable so later
// check-ins compare component-wise.
func cachedCheckInValue(currentUpdateID string, failedFP string, deviceFP string) string {
	return "f:" + currentUpdateID + ":" + failedFP + ":" + deviceFP
}

// Values written before the hardware component existed have two fields; they
// parse with an unknown device fingerprint, so the next telemetry batch writes
// the hardware through instead of the entry looking already up to date.
func parseCachedCheckIn(value string) (currentUpdateID string, failedFP string, deviceFP string, ok bool) {
	rest, found := strings.CutPrefix(value, "f:")
	if !found {
		return "", "", "", false
	}
	currentUpdateID, rest, ok = strings.Cut(rest, ":")
	if !ok {
		return "", "", "", false
	}
	failedFP, deviceFP, _ = strings.Cut(rest, ":")
	return currentUpdateID, failedFP, deviceFP, true
}

// touchTimeout bounds the background registration a check-in triggers.
const touchTimeout = 5 * time.Second

// Record records one device check-in, debounced against the last
// RECORDED state. Both ids are raw header input on the manifest path (no
// app-existence middleware there), so non-UUIDs are ignored outright. The
// cache check stays inline (a map or Redis read); only a real change (or an
// expired entry) spawns the background write, detached from the request on
// purpose (WithoutCancel, like audit's recorder) so a canceled poll still
// counts.
//
// A check-in that does not know a component ("" current from embedded-bundle
// telemetry, no failure list on any telemetry) never busts the debounce on
// that component: unknown is not a state, it is an absence of signal.
func (r *CheckInRecorder) Record(ctx context.Context, checkIn handlers.DeviceCheckIn) {
	if _, err := uuid.Parse(checkIn.AppID); err != nil {
		return
	}
	if _, err := uuid.Parse(checkIn.EASClientID); err != nil {
		return
	}
	// A refused poll leaves nothing durable. Its crash detail is stashed, which
	// is where an unsaved one already waits (see record below), so the next
	// poll that does resolve writes it onto the failure it belongs to. Nothing
	// else of a refused poll is worth keeping: every other signal survives on
	// its own.
	if checkIn.Rejected {
		if checkIn.FatalError != "" {
			ttl := fatalStashTTLSeconds
			_ = r.cache.Set(fatalStashKey(checkIn.AppID, checkIn.EASClientID), boundFatalError(checkIn.FatalError), &ttl)
		}
		return
	}
	state := normalizeCheckIn(checkIn, time.Now().UTC())
	key := checkInCacheKey(checkIn.AppID, checkIn.EASClientID)
	cached := r.cache.Get(key)

	// Error cooldown: one doomed attempt per TTL. Exception: a poll carrying
	// the one-shot fatal error always tries, losing the crash detail to a
	// backoff would be permanent while one extra failed write is nothing.
	if cached == checkInErrorCacheValue && state.fatalError == "" {
		return
	}

	needsWrite := cached == "" || cached == checkInErrorCacheValue || state.fatalError != ""
	cachedCurrent, cachedFailedFP, cachedDeviceFP := "", "", ""
	stateDeviceFP := deviceFingerprint(state.device)
	if !needsWrite {
		var parsed bool
		cachedCurrent, cachedFailedFP, cachedDeviceFP, parsed = parseCachedCheckIn(cached)
		switch {
		case !parsed:
			needsWrite = true
		case state.currentUpdateID != "" && state.currentUpdateID != cachedCurrent:
			needsWrite = true
		case len(state.failedUpdateIDs) > 0 && failedFingerprint(state.failedUpdateIDs) != cachedFailedFP:
			needsWrite = true
		case stateDeviceFP != "" && stateDeviceFP != cachedDeviceFP:
			needsWrite = true
		}
	}
	if !needsWrite {
		return
	}

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), touchTimeout)
	go func() {
		defer cancel()
		ttl := checkInTTLSeconds
		if err := r.record(bgCtx, checkIn, state); err != nil {
			log.Printf("observe: device check-in registration failed: %v", err)
			_ = r.cache.Set(key, checkInErrorCacheValue, &ttl)
			return
		}
		// The recorded state: a component this check-in did not know keeps
		// its previously recorded value, so the next knowing check-in
		// compares against reality.
		newCurrent := state.currentUpdateID
		if newCurrent == "" {
			newCurrent = cachedCurrent
		}
		newFailedFP := cachedFailedFP
		if len(state.failedUpdateIDs) > 0 || newFailedFP == "" {
			newFailedFP = failedFingerprint(state.failedUpdateIDs)
		}
		newDeviceFP := stateDeviceFP
		if newDeviceFP == "" {
			newDeviceFP = cachedDeviceFP
		}
		_ = r.cache.Set(key, cachedCheckInValue(newCurrent, newFailedFP, newDeviceFP), &ttl)
	}()
}

// record persists one check-in. Failures go FIRST: the fatal error is the
// only unrecoverable datum (the client sends it once), so it must not be
// lost to a touch that fails midway; device_update_failures has no FK on
// device_identity, so the order is safe. An unsaved fatal error is stashed
// and re-attached to the next successful failure write.
func (r *CheckInRecorder) record(ctx context.Context, checkIn handlers.DeviceCheckIn, state checkInState) error {
	if len(state.failedUpdateIDs) > 0 {
		fatal := state.fatalError
		stash := fatalStashKey(checkIn.AppID, checkIn.EASClientID)
		if fatal == "" {
			fatal = r.cache.Get(stash)
		}
		if err := r.identity.RecordUpdateFailures(ctx, checkIn.AppID, checkIn.EASClientID, state.failedUpdateIDs, fatal, identity.FailureTypeUpdate); err != nil {
			if state.fatalError != "" {
				stashTTL := fatalStashTTLSeconds
				_ = r.cache.Set(stash, state.fatalError, &stashTTL)
			}
			return err
		}
		r.cache.Delete(stash)
	}

	var currentUpdate *identity.CurrentUpdate
	if state.currentUpdateID != "" {
		currentUpdate = &identity.CurrentUpdate{ID: state.currentUpdateID, ObservedAt: state.observedAt}
	}
	return r.identity.TouchDevice(ctx, checkIn.AppID, checkIn.EASClientID, checkIn.RemoteIP, currentUpdate, state.device)
}

const maxFailedUpdateIDsPerCheckIn = 5

// ParseFailedUpdateIDs reads the Expo-Recent-Failed-Update-IDs header: a
// structured-field list of quoted lowercase UUIDs (`"id1", "id2"`, RFC 8941
// serialization of expo-updates' StringList). Hand-parsed: values are plain
// quoted strings with no parameters, and tolerance matters more than spec
// completeness on an unauthenticated header. Anything that does not parse as
// a UUID is dropped; output is canonical lowercase, deduplicated and capped so
// one unauthenticated manifest poll cannot fan out into unbounded SQL writes.
func ParseFailedUpdateIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	ids := make([]string, 0, maxFailedUpdateIDsPerCheckIn)
	seen := make(map[string]struct{}, maxFailedUpdateIDsPerCheckIn)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		parsed, err := uuid.Parse(part)
		if err != nil {
			continue
		}
		id := parsed.String()
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		if len(ids) == maxFailedUpdateIDsPerCheckIn {
			break
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}
