// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"log"
	"time"

	"expo-open-ota/ee/observe"
	mittools "expo-open-ota/internal/mcptools"
	"expo-open-ota/internal/types"

	"slices"

	"github.com/google/uuid"
	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// maxHealthUpdateIDs matches what the dashboard route accepts.
	maxHealthUpdateIDs = 20
	// The snapshots are one-minute buckets, so a day holds 1440 of them per
	// update. A caller reading them needs a readable curve, not the raw grid.
	defaultHealthPoints = 24
	maxHealthPoints     = 60
	defaultHealthWindow = 24 * time.Hour
	maxHealthWindow     = 90 * 24 * time.Hour
)

type GetUpdateHealthInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	// One of UpdateUUIDs or PublishGroup identifies what to report on.
	UpdateUUIDs  []string `json:"updateUUIDs,omitempty" jsonschema:"the updates to report on, as returned by get_updates (up to 20); their cohorts are summed together"`
	PublishGroup string   `json:"publishGroup,omitempty" jsonschema:"report on every update of this publish group instead of naming them, as returned by get_updates"`
	From         string   `json:"from,omitempty" jsonschema:"start of the window, RFC3339; defaults to 24 hours before to"`
	To           string   `json:"to,omitempty" jsonschema:"end of the window, RFC3339; defaults to now"`
	MaxPoints    int      `json:"maxPoints,omitempty" jsonschema:"how many points the curve carries; default 24, max 60. The snapshots are per-minute, so they are resampled down to this"`
}

// CurrentHealth is the instant-T state read from the device registry, the same
// numbers the dashboard badges show.
type CurrentHealth struct {
	DevicesOnUpdate   int64    `json:"devicesOnUpdate" jsonschema:"devices whose current update is one of these; adoption right now, not cumulative reach"`
	SuccessfulDevices int64    `json:"successfulDevices" jsonschema:"devices on the update holding no unresolved fault for it"`
	FaultyDevices     int64    `json:"faultyDevices" jsonschema:"distinct devices with an unresolved fault for the update, whether or not they are still on it"`
	UpdateIssues      int64    `json:"updateIssues" jsonschema:"faults where the bundle could not launch and the client rolled it back (the dashboard calls these native)"`
	RuntimeIssues     int64    `json:"runtimeIssues" jsonschema:"faults where the bundle ran and then crashed in JS; a device reporting both kinds counts in each, so the two can exceed faultyDevices"`
	HealthPercent     *float64 `json:"healthPercent" jsonschema:"successfulDevices over the devices that attempted the update; null when none attempted"`
}

// HealthPoint is one bucket of the snapshot history.
type HealthPoint struct {
	Timestamp         string   `json:"timestamp"`
	DevicesOnUpdate   uint64   `json:"devicesOnUpdate"`
	SuccessfulDevices uint64   `json:"successfulDevices"`
	FaultyDevices     uint64   `json:"faultyDevices"`
	UpdateIssues      uint64   `json:"updateIssues"`
	RuntimeIssues     uint64   `json:"runtimeIssues"`
	HealthPercent     *float64 `json:"healthPercent"`
}

// ArrivalPoint is the degraded history served without ClickHouse: it counts
// arrivals, which never go down, and devices failing at that instant.
type ArrivalPoint struct {
	Timestamp      string `json:"timestamp"`
	ArrivedDevices uint64 `json:"arrivedDevices"`
	FailingDevices uint64 `json:"failingDevices"`
}

type GetUpdateHealthOutput struct {
	UpdateUUIDs []string `json:"updateUUIDs" jsonschema:"the updates this answer covers, cohorts summed"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	// Current is absent when the device registry is unavailable.
	Current *CurrentHealth `json:"current,omitempty"`
	// Source says which history the deployment could serve, and therefore
	// which of the two series below is filled.
	Source   string         `json:"source" jsonschema:"snapshots (full health history), arrivals (degraded: no ClickHouse, adoption only and monotonic), or unavailable"`
	Note     string         `json:"note,omitempty" jsonschema:"why the history is degraded or unavailable"`
	History  []HealthPoint  `json:"history,omitempty" jsonschema:"one point per resampled bucket, oldest first; set when source is snapshots"`
	Arrivals []ArrivalPoint `json:"arrivals,omitempty" jsonschema:"set when source is arrivals: arrivedDevices only ever grows, so a rollback reads as a plateau rather than a drop"`
}

func getUpdateHealthHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdateHealthInput) (*mcpprot.CallToolResult, GetUpdateHealthOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetUpdateHealthInput) (*mcpprot.CallToolResult, GetUpdateHealthOutput, error) {
		principal := mittools.PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetUpdateHealthOutput{}, errors.New("no authenticated account on this session")
		}
		// The dashboard twin of this read is AnyViewer: seeing the app is
		// enough, no permission gates it.
		if err := mittools.RequireAppAccess(ctx, principal, input.AppId, deps.Apps, deps.VisibleApps); err != nil {
			return nil, GetUpdateHealthOutput{}, err
		}

		updateUUIDs, err := deps.resolveHealthTargets(ctx, input)
		if err != nil {
			return nil, GetUpdateHealthOutput{}, err
		}
		from, to, err := healthWindow(input.From, input.To)
		if err != nil {
			return nil, GetUpdateHealthOutput{}, err
		}
		points := input.MaxPoints
		if points <= 0 {
			points = defaultHealthPoints
		}
		if points > maxHealthPoints {
			points = maxHealthPoints
		}

		output := GetUpdateHealthOutput{
			UpdateUUIDs: updateUUIDs,
			From:        from.Format(time.RFC3339),
			To:          to.Format(time.RFC3339),
			Source:      "unavailable",
		}

		ctx, cancel := boundedRead(ctx)
		defer cancel()
		if deps.Identity != nil {
			health, err := deps.Identity.UpdateHealthByIDs(ctx, input.AppId, updateUUIDs)
			if err != nil {
				log.Printf("mcp get_update_health could not read the device registry of app %s: %v", input.AppId, err)
			} else {
				output.Current = sumCurrentHealth(health)
			}
		}

		switch {
		case deps.HealthHistory != nil:
			series, err := deps.HealthHistory.Read(ctx, input.AppId, updateUUIDs, from, to)
			if err != nil {
				log.Printf("mcp get_update_health could not read the health history of app %s: %v", input.AppId, err)
				return nil, GetUpdateHealthOutput{}, errors.New("could not read the health history, try again later")
			}
			output.Source = "snapshots"
			output.History = resampleHealth(sumHealthSeries(series), points)
			if len(output.History) == 0 {
				output.Note = "no snapshot was taken in this window; the first one lands a minute after devices start reporting"
			}
		case deps.StateHistory != nil:
			series, err := deps.StateHistory.Read(ctx, input.AppId, updateUUIDs, from, to)
			if err != nil {
				log.Printf("mcp get_update_health could not read the arrival history of app %s: %v", input.AppId, err)
				return nil, GetUpdateHealthOutput{}, errors.New("could not read the arrival history, try again later")
			}
			output.Source = "arrivals"
			output.Note = "this deployment has no ClickHouse configured, so the full health history is unavailable; these are device arrivals and current failures"
			output.Arrivals = resampleArrivals(sumArrivalSeries(series), points)
		default:
			output.Note = "this deployment records nothing about devices (stateless mode, or device telemetry disabled), so no history exists"
		}
		return nil, output, nil
	}
}

// resolveHealthTargets turns the input into the update uuids to report on. A
// publish group is resolved through the update feed, the same two-step the
// dashboard does, because no server-side read aggregates a group's health.
func (deps Deps) resolveHealthTargets(ctx context.Context, input GetUpdateHealthInput) ([]string, error) {
	if (len(input.UpdateUUIDs) == 0) == (input.PublishGroup == "") {
		return nil, errors.New("provide either updateUUIDs or publishGroup, not both")
	}
	if input.PublishGroup != "" {
		if _, err := uuid.Parse(input.PublishGroup); err != nil {
			return nil, errors.New("publishGroup must be a UUID, as returned by get_updates")
		}
		if deps.UpdateFeed == nil {
			return nil, errors.New("resolving a publish group requires the control plane; this deployment runs in stateless mode")
		}
		updates, err := deps.UpdateFeed.GetUpdateFeed(ctx, input.AppId, types.UpdateFeedQuery{
			PublishGroup: input.PublishGroup,
			Limit:        maxHealthUpdateIDs + 1,
		})
		if err != nil {
			log.Printf("mcp get_update_health could not resolve publish group %s of app %s: %v", input.PublishGroup, input.AppId, err)
			return nil, errors.New("could not resolve the publish group, try again later")
		}
		uuids := make([]string, 0, len(updates))
		for _, update := range updates {
			// A rollback marker carries no uuid to report on.
			if _, err := uuid.Parse(update.UpdateUUID); err != nil {
				continue
			}
			uuids = append(uuids, update.UpdateUUID)
		}
		if len(uuids) == 0 {
			return nil, errors.New("no update with health data in publish group " + input.PublishGroup)
		}
		return normalizeHealthUUIDs(uuids)
	}
	return normalizeHealthUUIDs(input.UpdateUUIDs)
}

func normalizeHealthUUIDs(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	uuids := make([]string, 0, len(raw))
	for _, value := range raw {
		parsed, err := uuid.Parse(value)
		if err != nil {
			return nil, errors.New("updateUUIDs must be update uuids, as returned by get_updates (the updateUUID field, not updateId)")
		}
		canonical := parsed.String()
		if seen[canonical] {
			continue
		}
		seen[canonical] = true
		uuids = append(uuids, canonical)
	}
	if len(uuids) == 0 || len(uuids) > maxHealthUpdateIDs {
		return nil, errors.New("provide between 1 and 20 update uuids")
	}
	return uuids, nil
}

func healthWindow(rawFrom string, rawTo string) (time.Time, time.Time, error) {
	to := time.Now().UTC()
	if rawTo != "" {
		parsed, err := time.Parse(time.RFC3339, rawTo)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("to must be an RFC3339 timestamp")
		}
		to = parsed.UTC()
	}
	from := to.Add(-defaultHealthWindow)
	if rawFrom != "" {
		parsed, err := time.Parse(time.RFC3339, rawFrom)
		if err != nil {
			return time.Time{}, time.Time{}, errors.New("from must be an RFC3339 timestamp")
		}
		from = parsed.UTC()
	}
	if !from.Before(to) || to.Sub(from) > maxHealthWindow {
		return time.Time{}, time.Time{}, errors.New("from must be earlier than to, and within 90 days of it")
	}
	return from, to, nil
}

// sumCurrentHealth adds the cohorts of every update, then recomputes the
// percentage: averaging per-update percentages would weigh a ten-device update
// like a ten-thousand-device one.
func sumCurrentHealth(health map[string]identityUpdateHealth) *CurrentHealth {
	current := CurrentHealth{}
	for _, entry := range health {
		successful := entry.DevicesOnUpdate - entry.FailedStillOn
		if successful < 0 {
			successful = 0
		}
		current.DevicesOnUpdate += entry.DevicesOnUpdate
		current.SuccessfulDevices += successful
		current.FaultyDevices += entry.FaultyDevices
		current.UpdateIssues += entry.UpdateIssues
		current.RuntimeIssues += entry.RuntimeIssues
	}
	if attempts := current.SuccessfulDevices + current.FaultyDevices; attempts > 0 {
		percent := 100 * float64(current.SuccessfulDevices) / float64(attempts)
		current.HealthPercent = &percent
	}
	return &current
}

// sumHealthSeries collapses the per-update series into one, bucket by bucket.
func sumHealthSeries(series map[string][]observe.HealthHistoryPoint) []HealthPoint {
	byTimestamp := map[time.Time]*HealthPoint{}
	for _, points := range series {
		for _, point := range points {
			bucket := byTimestamp[point.Timestamp]
			if bucket == nil {
				bucket = &HealthPoint{Timestamp: point.Timestamp.UTC().Format(time.RFC3339)}
				byTimestamp[point.Timestamp] = bucket
			}
			bucket.DevicesOnUpdate += point.DevicesOnUpdate
			bucket.SuccessfulDevices += point.SuccessfulDevices
			bucket.FaultyDevices += point.FaultyDevices
			bucket.UpdateIssues += point.UpdateIssues
			bucket.RuntimeIssues += point.RuntimeIssues
		}
	}
	timestamps := make([]time.Time, 0, len(byTimestamp))
	for timestamp := range byTimestamp {
		timestamps = append(timestamps, timestamp)
	}
	slices.SortFunc(timestamps, func(a, b time.Time) int { return a.Compare(b) })
	summed := make([]HealthPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		point := *byTimestamp[timestamp]
		if attempts := point.SuccessfulDevices + point.FaultyDevices; attempts > 0 {
			percent := 100 * float64(point.SuccessfulDevices) / float64(attempts)
			point.HealthPercent = &percent
		}
		summed = append(summed, point)
	}
	return summed
}

func sumArrivalSeries(series map[string][]observe.StateHistoryPoint) []ArrivalPoint {
	byTimestamp := map[time.Time]*ArrivalPoint{}
	for _, points := range series {
		for _, point := range points {
			bucket := byTimestamp[point.Timestamp]
			if bucket == nil {
				bucket = &ArrivalPoint{Timestamp: point.Timestamp.UTC().Format(time.RFC3339)}
				byTimestamp[point.Timestamp] = bucket
			}
			bucket.ArrivedDevices += point.ArrivedDevices
			bucket.FailingDevices += point.FailingDevices
		}
	}
	timestamps := make([]time.Time, 0, len(byTimestamp))
	for timestamp := range byTimestamp {
		timestamps = append(timestamps, timestamp)
	}
	slices.SortFunc(timestamps, func(a, b time.Time) int { return a.Compare(b) })
	summed := make([]ArrivalPoint, 0, len(timestamps))
	for _, timestamp := range timestamps {
		summed = append(summed, *byTimestamp[timestamp])
	}
	return summed
}

// resampleHealth keeps the last point of each of at most max slices, the same
// last-value-per-bucket the dashboard chart uses: a health curve read at a
// coarser step must show where it ended, not an average that hides a dip.
func resampleHealth(points []HealthPoint, max int) []HealthPoint {
	kept := make([]HealthPoint, 0, min(len(points), max))
	for _, index := range resampleIndexes(len(points), max) {
		kept = append(kept, points[index])
	}
	return kept
}

func resampleArrivals(points []ArrivalPoint, max int) []ArrivalPoint {
	kept := make([]ArrivalPoint, 0, min(len(points), max))
	for _, index := range resampleIndexes(len(points), max) {
		kept = append(kept, points[index])
	}
	return kept
}

// resampleIndexes names the points to keep: the last of each even slice, and
// always the most recent one.
func resampleIndexes(length int, max int) []int {
	if length == 0 {
		return nil
	}
	if length <= max {
		indexes := make([]int, length)
		for i := range indexes {
			indexes[i] = i
		}
		return indexes
	}
	indexes := make([]int, 0, max)
	for slice := 1; slice <= max; slice++ {
		index := slice*length/max - 1
		if index < 0 {
			index = 0
		}
		if len(indexes) > 0 && indexes[len(indexes)-1] == index {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

func registerGetUpdateHealth(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "get_update_health",
		Description: "The health of one or more updates, or of a whole publish group: how many devices adopted it, how many are failing, the launch and JS crash counts, and how all of that moved over a window. " +
			"Cohorts of several updates are summed, never averaged. Read source to know what the history means: snapshots is the real health history, arrivals is the degraded one this deployment can serve without ClickHouse.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Update health", ReadOnlyHint: true},
	}, getUpdateHealthHandler(deps))
}
