// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package mcptools

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"

	"xprem/ee/identity"
	"xprem/ee/rbac"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// identityAccess is the device routes' own permission, fallback included.
var identityAccess = rbac.MCPAccess(rbac.PermIdentityRead)

const (
	// A device carries a dozen fields plus its attributes, so the page an
	// agent reads is deliberately smaller than the dashboard's.
	defaultDevicesPage = 20
	maxDevicesPage     = 100
)

// DeviceFilters is the shared filter set of the device tools. Every list is a
// disjunction, and the lists are combined with AND.
type DeviceFilters struct {
	EasClientIds    []string `json:"easClientIds,omitempty" jsonschema:"exact install uuids"`
	UpdateIds       []string `json:"updateIds,omitempty" jsonschema:"devices currently running one of these update uuids, as returned by get_updates"`
	UpdateGroupIds  []string `json:"updateGroupIds,omitempty" jsonschema:"devices currently running an update of one of these publish groups"`
	Branches        []string `json:"branches,omitempty" jsonschema:"branch names, as returned by get_branches"`
	RuntimeVersions []string `json:"runtimeVersions,omitempty" jsonschema:"runtime versions, as returned by get_runtime_versions"`
	Platforms       []string `json:"platforms,omitempty" jsonschema:"ios or android"`
	DeviceModels    []string `json:"deviceModels,omitempty" jsonschema:"hardware models as the devices reported them"`
	OsNames         []string `json:"osNames,omitempty"`
	OsVersions      []string `json:"osVersions,omitempty"`
	CountryCodes    []string `json:"countryCodes,omitempty" jsonschema:"two-letter country codes resolved from the device address"`
	Attributes      []string `json:"attributes,omitempty" jsonschema:"operator-defined attributes as key:value pairs, e.g. plan:pro; the key must be declared, list them with get_device_attributes. Repeating a key matches any of its values, different keys must all match"`
}

type SearchDevicesInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	DeviceFilters
	Limit  int    `json:"limit,omitempty" jsonschema:"devices per page; default 20, max 100"`
	Cursor string `json:"cursor,omitempty" jsonschema:"page forward: pass the nextCursor of a previous answer"`
}

// DeviceOutput is one install, as the dashboard device list shows it.
type DeviceOutput struct {
	EasClientId     string         `json:"easClientId"`
	Platform        *string        `json:"platform,omitempty"`
	Branch          *string        `json:"branch,omitempty"`
	RuntimeVersion  *string        `json:"runtimeVersion,omitempty"`
	CurrentUpdateId *string        `json:"currentUpdateId,omitempty" jsonschema:"the update this device is running; absent when this server never published it"`
	DeviceModel     *string        `json:"deviceModel,omitempty"`
	OsName          *string        `json:"osName,omitempty"`
	OsVersion       *string        `json:"osVersion,omitempty"`
	CountryCode     *string        `json:"countryCode,omitempty"`
	City            *string        `json:"city,omitempty"`
	Attributes      map[string]any `json:"attributes" jsonschema:"the operator-defined attributes this install reported"`
	FirstSeenAt     string         `json:"firstSeenAt"`
	LastSeenAt      string         `json:"lastSeenAt"`
}

type SearchDevicesOutput struct {
	Devices []DeviceOutput `json:"devices" jsonschema:"most recently seen first"`
	// NextCursor is absent on the last page.
	NextCursor string `json:"nextCursor,omitempty" jsonschema:"pass it back as cursor for the next page; it is only valid for this exact set of filters"`
}

func deviceOutput(device identity.Device) DeviceOutput {
	attributes := device.Metadata
	if attributes == nil {
		attributes = map[string]any{}
	}
	return DeviceOutput{
		EasClientId:     device.EASClientID,
		Platform:        device.Platform,
		Branch:          device.Branch,
		RuntimeVersion:  device.RuntimeVersion,
		CurrentUpdateId: device.CurrentUpdateID,
		DeviceModel:     device.DeviceModel,
		OsName:          device.OSName,
		OsVersion:       device.OSVersion,
		CountryCode:     device.CountryCode,
		City:            device.City,
		Attributes:      attributes,
		FirstSeenAt:     device.FirstSeenAt.UTC().Format(time.RFC3339),
		LastSeenAt:      device.LastSeenAt.UTC().Format(time.RFC3339),
	}
}

// validate applies the identity route's caps and uuid checks.
func (filters DeviceFilters) validate() error {
	for _, list := range []struct {
		name   string
		values []string
		uuid   bool
	}{
		{"easClientIds", filters.EasClientIds, true},
		{"updateIds", filters.UpdateIds, true},
		{"updateGroupIds", filters.UpdateGroupIds, true},
		{"branches", filters.Branches, false},
		{"runtimeVersions", filters.RuntimeVersions, false},
		{"platforms", filters.Platforms, false},
		{"deviceModels", filters.DeviceModels, false},
		{"osNames", filters.OsNames, false},
		{"osVersions", filters.OsVersions, false},
		{"countryCodes", filters.CountryCodes, false},
		{"attributes", filters.Attributes, false},
	} {
		if err := validateFilterList(list.name, list.values, maxDeviceFilterValues, list.uuid); err != nil {
			return err
		}
	}
	return nil
}

// deviceQuery turns the tool filters into the registry query, resolving the
// attribute pairs against the app's declared schema so a typo names itself
// instead of silently matching nothing.
func (deps Deps) deviceQuery(ctx context.Context, appID string, filters DeviceFilters) (identity.DeviceQuery, error) {
	if err := filters.validate(); err != nil {
		return identity.DeviceQuery{}, err
	}
	query := identity.DeviceQuery{
		EASClientIDs:     filters.EasClientIds,
		CurrentUpdateIDs: filters.UpdateIds,
		UpdateGroupIDs:   filters.UpdateGroupIds,
		Branches:         filters.Branches,
		RuntimeVersions:  filters.RuntimeVersions,
		Platforms:        filters.Platforms,
		DeviceModels:     filters.DeviceModels,
		OSNames:          filters.OsNames,
		OSVersions:       filters.OsVersions,
		CountryCodes:     filters.CountryCodes,
	}
	if len(filters.Attributes) == 0 {
		return query, nil
	}
	schema, err := deps.Identity.GetSchema(ctx, appID)
	if err != nil {
		log.Printf("mcp device tools could not read the attribute schema of app %s: %v", appID, err)
		return identity.DeviceQuery{}, errors.New("could not read the attribute schema, try again later")
	}
	metadata, err := identity.ParseFilterPairs(schema, filters.Attributes)
	if err != nil {
		return identity.DeviceQuery{}, errors.New("attributes must be key:value pairs of declared attributes, and the value must match the declared type; list them with get_device_attributes")
	}
	query.Metadata = metadata
	return query, nil
}

func searchDevicesHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input SearchDevicesInput) (*mcpprot.CallToolResult, SearchDevicesOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input SearchDevicesInput) (*mcpprot.CallToolResult, SearchDevicesOutput, error) {
		if err := deps.requireDeviceRegistry(ctx, req, input.AppId); err != nil {
			return nil, SearchDevicesOutput{}, err
		}
		query, err := deps.deviceQuery(ctx, input.AppId, input.DeviceFilters)
		if err != nil {
			return nil, SearchDevicesOutput{}, err
		}
		cursor, err := identity.DecodeDeviceCursor(input.Cursor)
		if err != nil {
			return nil, SearchDevicesOutput{}, errors.New("cursor is invalid; pass a nextCursor from a previous answer")
		}
		limit := input.Limit
		if limit <= 0 {
			limit = defaultDevicesPage
		}
		if limit > maxDevicesPage {
			limit = maxDevicesPage
		}

		ctx, cancel := boundedRead(ctx)
		defer cancel()
		devices, next, err := deps.Identity.ListDevices(ctx, input.AppId, query, limit, cursor)
		if err != nil {
			if errors.Is(err, identity.ErrTooManyCombinations) {
				return nil, SearchDevicesOutput{}, errors.New("too many attribute combinations; narrow the attributes filter")
			}
			log.Printf("mcp search_devices failed for app %s: %v", input.AppId, err)
			return nil, SearchDevicesOutput{}, errors.New("could not search the devices, try again later")
		}
		output := SearchDevicesOutput{Devices: make([]DeviceOutput, 0, len(devices)), NextCursor: identity.EncodeDeviceCursor(next)}
		for _, device := range devices {
			output.Devices = append(output.Devices, deviceOutput(device))
		}
		return nil, output, nil
	}
}

func registerSearchDevices(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "search_devices",
		Description: "Search the device registry of an app: which installs are running what, where they are, and the operator-defined attributes they carry. " +
			"Every filter is optional and they combine with AND; most recently seen first, paginated by cursor. Requires the identity:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Search devices", ReadOnlyHint: true},
	}, searchDevicesHandler(deps))
}

type GetDeviceInput struct {
	AppId       string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	EasClientId string `json:"easClientId" jsonschema:"the install uuid, as returned by search_devices"`
}

func getDeviceHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetDeviceInput) (*mcpprot.CallToolResult, DeviceOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetDeviceInput) (*mcpprot.CallToolResult, DeviceOutput, error) {
		if err := deps.requireDeviceRegistry(ctx, req, input.AppId); err != nil {
			return nil, DeviceOutput{}, err
		}
		if input.EasClientId == "" {
			return nil, DeviceOutput{}, errors.New("easClientId is required; find it with search_devices")
		}
		device, err := deps.Identity.GetDevice(ctx, input.AppId, input.EasClientId)
		if err != nil {
			log.Printf("mcp get_device failed for app %s: %v", input.AppId, err)
			return nil, DeviceOutput{}, errors.New("could not read the device, try again later")
		}
		if device == nil {
			return nil, DeviceOutput{}, errors.New("no device with this easClientId on this app")
		}
		return nil, deviceOutput(*device), nil
	}
}

func registerGetDevice(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_device",
		Description: "One install of an app by its easClientId: what it runs, where it is, and its attributes. Requires the identity:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Device detail", ReadOnlyHint: true},
	}, getDeviceHandler(deps))
}

type CountOnlineDevicesInput struct {
	AppId string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	DeviceFilters
	WindowMinutes int `json:"windowMinutes,omitempty" jsonschema:"how recently a device must have contacted the server to count as online; default 20 minutes, max 24 hours"`
}

type CountOnlineDevicesOutput struct {
	Online        int64 `json:"online"`
	WindowMinutes int   `json:"windowMinutes" jsonschema:"the window actually applied after clamping"`
}

func countOnlineDevicesHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input CountOnlineDevicesInput) (*mcpprot.CallToolResult, CountOnlineDevicesOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input CountOnlineDevicesInput) (*mcpprot.CallToolResult, CountOnlineDevicesOutput, error) {
		if err := deps.requireDeviceRegistry(ctx, req, input.AppId); err != nil {
			return nil, CountOnlineDevicesOutput{}, err
		}
		if input.WindowMinutes < 0 {
			return nil, CountOnlineDevicesOutput{}, errors.New("windowMinutes must be positive")
		}
		query, err := deps.deviceQuery(ctx, input.AppId, input.DeviceFilters)
		if err != nil {
			return nil, CountOnlineDevicesOutput{}, err
		}
		window := time.Duration(input.WindowMinutes) * time.Minute
		if window <= 0 {
			window = identity.DefaultOnlineWindow
		}
		if window > identity.MaxOnlineWindow {
			window = identity.MaxOnlineWindow
		}
		ctx, cancel := boundedRead(ctx)
		defer cancel()
		online, err := deps.Identity.CountOnlineDevices(ctx, input.AppId, window, query)
		if err != nil {
			log.Printf("mcp count_online_devices failed for app %s: %v", input.AppId, err)
			return nil, CountOnlineDevicesOutput{}, errors.New("could not count the online devices, try again later")
		}
		return nil, CountOnlineDevicesOutput{Online: online, WindowMinutes: int(window / time.Minute)}, nil
	}
}

func registerCountOnlineDevices(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "count_online_devices",
		Description: "How many devices contacted the server recently, with the same filters as search_devices. Use it to size a cohort before searching it, or to watch an update land. Requires the identity:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Online devices", ReadOnlyHint: true},
	}, countOnlineDevicesHandler(deps))
}

type GetDeviceAttributesInput struct {
	AppId  string `json:"appId" jsonschema:"the app id, as returned by get_apps"`
	Key    string `json:"key,omitempty" jsonschema:"when set, return the values seen for this attribute instead of the declared keys"`
	Search string `json:"search,omitempty" jsonschema:"only values containing this text; requires key"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many values to return; default 20, max 100"`
}

type DeviceAttributeKey struct {
	Key       string `json:"key"`
	Type      string `json:"type" jsonschema:"string, number or boolean; a filter value must match this type"`
	MaxLength int    `json:"maxLength"`
}

type DeviceAttributeValue struct {
	Value       string `json:"value"`
	DeviceCount int64  `json:"deviceCount"`
}

type GetDeviceAttributesOutput struct {
	Keys   []DeviceAttributeKey   `json:"keys,omitempty" jsonschema:"the attributes this app declares; devices report nothing else"`
	Values []DeviceAttributeValue `json:"values,omitempty" jsonschema:"set when key was given: the values seen for it, most devices first"`
}

func getDeviceAttributesHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, input GetDeviceAttributesInput) (*mcpprot.CallToolResult, GetDeviceAttributesOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, input GetDeviceAttributesInput) (*mcpprot.CallToolResult, GetDeviceAttributesOutput, error) {
		if err := deps.requireDeviceRegistry(ctx, req, input.AppId); err != nil {
			return nil, GetDeviceAttributesOutput{}, err
		}
		if input.Key == "" {
			if input.Search != "" {
				return nil, GetDeviceAttributesOutput{}, errors.New("search only applies with a key")
			}
			schema, err := deps.Identity.GetSchema(ctx, input.AppId)
			if err != nil {
				log.Printf("mcp get_device_attributes failed for app %s: %v", input.AppId, err)
				return nil, GetDeviceAttributesOutput{}, errors.New("could not read the attribute schema, try again later")
			}
			output := GetDeviceAttributesOutput{Keys: make([]DeviceAttributeKey, 0, len(schema))}
			for _, spec := range schema {
				output.Keys = append(output.Keys, DeviceAttributeKey{Key: spec.Key, Type: string(spec.Type), MaxLength: spec.MaxLength})
			}
			slices.SortFunc(output.Keys, func(left, right DeviceAttributeKey) int {
				return strings.Compare(left.Key, right.Key)
			})
			return nil, output, nil
		}

		limit := input.Limit
		if limit <= 0 {
			limit = 20
		}
		values, err := deps.Identity.SearchMetadataValues(ctx, input.AppId, input.Key, input.Search, limit)
		if err != nil {
			log.Printf("mcp get_device_attributes failed for app %s key %s: %v", input.AppId, input.Key, err)
			return nil, GetDeviceAttributesOutput{}, errors.New("could not read the attribute values, try again later")
		}
		output := GetDeviceAttributesOutput{Values: make([]DeviceAttributeValue, 0, len(values))}
		for _, value := range values {
			output.Values = append(output.Values, DeviceAttributeValue{Value: value.Value, DeviceCount: value.DeviceCount})
		}
		return nil, output, nil
	}
}

func registerGetDeviceAttributes(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name: "get_device_attributes",
		Description: "The operator-defined attributes an app declares (key and type), or the values seen for one of them with how many devices carry each. " +
			"Call it before filtering search_devices by attributes: only declared keys can be filtered, and the value must match the declared type. Requires the identity:read permission.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Device attributes", ReadOnlyHint: true},
	}, getDeviceAttributesHandler(deps))
}

// requireDeviceRegistry is the shared gate of the device tools: a caller
// holding identity:read on this app, on a deployment that records devices.
func (deps Deps) requireDeviceRegistry(ctx context.Context, req *mcpprot.CallToolRequest, appID string) error {
	if err := deps.requireAppPermission(ctx, req, appID, identityAccess); err != nil {
		return err
	}
	if deps.Identity == nil {
		return errors.New("this deployment records nothing about devices (stateless mode, or device telemetry disabled)")
	}
	return nil
}
