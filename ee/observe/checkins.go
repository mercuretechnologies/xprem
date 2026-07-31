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

// CheckInRecorder records device check-ins (existence and update-health
// state) into the device identity registry, debounced against the last
// recorded state to bound the write rate.
type CheckInRecorder struct {
	identity *identity.Service
	cache    cache.Cache
}

func NewCheckInRecorder(identityService *identity.Service, c cache.Cache) *CheckInRecorder {
	return &CheckInRecorder{identity: identityService, cache: c}
}

// checkInTTLSeconds bounds the steady-state last_seen bump rate per device.
const checkInTTLSeconds = 60

// checkInErrorCacheValue marks a failed registration so retries back off for the TTL.
const checkInErrorCacheValue = "e"

// fatalStashTTLSeconds keeps an unsaved fatal error around long enough to
// survive a registry outage, so it can re-attach to the next successful failure write.
const fatalStashTTLSeconds = 3600

func checkInCacheKey(appID, easClientID string) string {
	return "observe:checkin:" + appID + ":" + easClientID
}

// checkInClaimTTLSeconds bounds a claim, longer than touchTimeout so a slow
// write is never preempted but a killed process frees the device quickly.
const checkInClaimTTLSeconds = 10

// checkInClaimKey marks that a device is currently being written.
func checkInClaimKey(appID, easClientID string) string {
	return "observe:checkin:claim:" + appID + ":" + easClientID
}

func fatalStashKey(appID, easClientID string) string {
	return "observe:fatal:" + appID + ":" + easClientID
}

// maxFatalErrorRunes bounds the crash detail before it is stored anywhere.
const maxFatalErrorRunes = 4096

// boundFatalError caps the crash detail at maxFatalErrorRunes, counted in
// runes so a multi-byte message is never cut mid-character.
func boundFatalError(detail string) string {
	if runes := []rune(detail); len(runes) > maxFatalErrorRunes {
		return string(runes[:maxFatalErrorRunes])
	}
	return detail
}

// checkInState is a check-in reduced to its normalized, effective
// update-health state, so manifest and telemetry sources compare equal.
type checkInState struct {
	// currentUpdateID is "" when this check-in does not know it; "" never overwrites a known value downstream.
	currentUpdateID string
	// observedAt is when the device was running that update.
	observedAt time.Time
	// failedUpdateIDs is empty both when there are no failures and when this check-in does not know.
	failedUpdateIDs []string
	fatalError      string
	device          identity.DeviceInfo
}

func normalizeCheckIn(checkIn handlers.DeviceCheckIn, now time.Time) checkInState {
	state := checkInState{fatalError: boundFatalError(checkIn.FatalError), observedAt: checkIn.ObservedAt}
	// Client-supplied timestamps are clamped to never be in the future.
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

// deviceFingerprint condenses the reported hardware and store version.
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

// failedFingerprint condenses the normalized failure list. The "f" prefix
// keeps real cache values out of the error sentinel's ("e") value space.
func failedFingerprint(ids []string) string {
	h := fnv.New64a()
	for _, id := range ids {
		_, _ = h.Write([]byte(id))
		_, _ = h.Write([]byte{0})
	}
	return "f" + strconv.FormatUint(h.Sum64(), 36)
}

// cachedCheckInValue encodes the last recorded state so later check-ins can compare component-wise.
func cachedCheckInValue(currentUpdateID string, failedFP string, deviceFP string) string {
	return "f:" + currentUpdateID + ":" + failedFP + ":" + deviceFP
}

// Values written before the hardware component existed have two fields and
// parse with an unknown device fingerprint.
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

// Record records one device check-in, debounced against the last recorded
// state; only a real change spawns the background write. A component the
// check-in does not know never busts the debounce on that component.
func (r *CheckInRecorder) Record(ctx context.Context, checkIn handlers.DeviceCheckIn) {
	if _, err := uuid.Parse(checkIn.AppID); err != nil {
		return
	}
	if _, err := uuid.Parse(checkIn.EASClientID); err != nil {
		return
	}
	// A refused poll leaves nothing durable except its crash detail, stashed for the next resolving poll.
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

	// Error cooldown, except a poll carrying the one-shot fatal error always tries.
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

	claim := ""
	if state.fatalError == "" {
		key := checkInClaimKey(checkIn.AppID, checkIn.EASClientID)
		if taken, err := r.cache.TryLock(key, checkInClaimTTLSeconds); err != nil || !taken {
			return
		}
		claim = key
	}

	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), touchTimeout)
	go func() {
		defer cancel()
		// Released only after the debounce write below, and only if this poll took a claim.
		if claim != "" {
			defer r.cache.Delete(claim)
		}
		ttl := checkInTTLSeconds
		if err := r.record(bgCtx, checkIn, state); err != nil {
			log.Printf("observe: device check-in registration failed: %v", err)
			_ = r.cache.Set(key, checkInErrorCacheValue, &ttl)
			return
		}
		// A component this check-in did not know keeps its previously recorded value.
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

// record persists one check-in. Failures are written first since the fatal
// error is unrecoverable if lost; an unsaved one is stashed for retry.
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
	return r.identity.TouchDevice(ctx, checkIn.AppID, checkIn.EASClientID, currentUpdate, state.device)
}

const maxFailedUpdateIDsPerCheckIn = 5

// ParseFailedUpdateIDs reads the Expo-Recent-Failed-Update-IDs header, a
// quoted-UUID list (RFC 8941 style). Invalid entries are dropped and the
// result is deduplicated and capped.
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
