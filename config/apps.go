package config

import (
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// maxAppIdLen caps the app id length. Long enough for UUIDs (36), ULIDs (26),
// Expo project ids (36), and reasonable slug prefixes; short enough that a
// hostile or typo'd value can't DoS our path-building or map-keying paths.
const maxAppIdLen = 64

// reservedAppIds are names that collide with top-level HTTP routes. Gorilla
// mux resolves static routes before pattern routes, so a config with an app
// id of "dashboard" would never receive traffic on /{APP_ID}/…, routes would
// route to the dashboard static handler instead. Rejecting these at boot
// surfaces the misconfiguration before it becomes a silent outage.
var reservedAppIds = map[string]struct{}{
	"api":       {},
	"assets":    {},
	"auth":      {},
	"dashboard": {},
	"hc":        {},
	"manifest":  {},
	"metrics":   {},
}

// KeysMode identifies how an app's signing key pair is stored.
type KeysMode string

const (
	KeysModeLocal       KeysMode = "local"
	KeysModeAWSSM       KeysMode = "aws-secrets-manager"
	KeysModeEnvironment KeysMode = "environment"
	KeysModeDatabase    KeysMode = "database"
)

// KeysConfig is a tagged union: exactly one set of fields must be populated,
// matched by Mode. Validated at LoadConfig time, downstream code assumes a
// valid config and does not re-check invariants.
type KeysConfig struct {
	Mode KeysMode `json:"mode"`

	// mode=local
	PublicPath  string `json:"publicPath,omitempty"`
	PrivatePath string `json:"privatePath,omitempty"`

	// mode=aws-secrets-manager
	PublicSecretId  string `json:"publicSecretId,omitempty"`
	PrivateSecretId string `json:"privateSecretId,omitempty"`

	// mode=environment
	PublicB64  string `json:"publicB64,omitempty"`
	PrivateB64 string `json:"privateB64,omitempty"`

	// mode=database
	SealedPublicKey  string `json:"sealedPublicKey,omitempty"`
	SealedPrivateKey string `json:"sealedPrivateKey,omitempty"`
}

// AppConfig is the resolved configuration for one app. In stateless mode it
// is built from the flat env vars; in control-plane mode it is hydrated from
// a database row. Each app has its own identity (id, accessToken) and signing
// key pair. Name is optional and used purely as a display label in the
// dashboard, it does not participate in request routing, which always goes
// by Id.
type AppConfig struct {
	Id          string        `json:"id"`
	Name        string        `json:"name,omitempty"`
	AccessToken string        `json:"accessToken,omitempty"`
	Keys        KeysConfig    `json:"keys"`
	CreatedAt   time.Duration `json:"createTime,omitempty"`
}

// AppDescriptor is the public-safe view of an AppConfig (no token, no keys)
// intended for dashboard listings and anything else that needs to enumerate
// apps without touching secrets.
type AppDescriptor struct {
	Id   string `json:"id"`
	Name string `json:"name,omitempty"`
}

var (
	appsByIdMu sync.RWMutex
	appsById   map[string]*AppConfig
)

// LoadAppsFromFlatEnv resolves the single-app stateless config from the flat
// env vars (EXPO_APP_ID, EXPO_ACCESS_TOKEN, KEYS_STORAGE_TYPE and its mode-
// specific siblings), validates it, and caches the result in memory. This
// mirrors the v1 env layout unchanged, so a v1 install upgrades to v2 with
// zero config changes. Multi-app deployments are served by the control plane
// (DB mode), which loads apps from the database and never calls this.
//
// Must be called once from wire.go (stateless branch) before any handler
// resolves an app. Returns a non-nil error on any structural or semantic
// issue; callers are expected to log.Fatal on error.
func LoadAppsFromFlatEnv() error {
	apps, source, err := ReadAppsFromFlatEnv()
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		return fmt.Errorf("%s: must declare at least one app", source)
	}
	index := make(map[string]*AppConfig, len(apps))
	for i := range apps {
		app := &apps[i]
		if err := validateApp(app, i); err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if _, dup := index[app.Id]; dup {
			return fmt.Errorf("%s: duplicate app id %q", source, app.Id)
		}
		index[app.Id] = app
	}
	appsByIdMu.Lock()
	appsById = index
	appsByIdMu.Unlock()
	return nil
}

// ReadAppsFromFlatEnv returns the parsed (but not yet validated) single-app
// config plus a human-readable source tag used for error messages. It reads
// the legacy v1 variable names verbatim to preserve upgrade-in-place. Returned
// as a slice so callers (LoadAppsFromFlatEnv, the infra→DB migration) share one
// iteration shape whether the config carries one app (stateless) or many
// (control plane).
func ReadAppsFromFlatEnv() ([]AppConfig, string, error) {
	if appId := strings.TrimSpace(os.Getenv("EXPO_APP_ID")); appId != "" {
		return []AppConfig{parseFlatEnvApp(appId)}, "flat env (EXPO_APP_ID)", nil
	}
	return nil, "", fmt.Errorf("no app config found: set EXPO_APP_ID + EXPO_ACCESS_TOKEN + key vars (multi-app deployments are managed by the control plane / DB mode)")
}

// LegacyFallbackAppId names the app that manifest and asset requests arriving
// without an expo-app-id header belong to, or "" when no such app exists.
//
// v1 clients predate the header. It is baked into Expo.plist /
// AndroidManifest.xml at build time, so an already-installed v1 binary can
// never start sending it without a store release, and an OTA server that
// requires a store release to keep serving OTA updates has defeated its own
// purpose. EXPO_APP_ID is the Expo project id v1 already identified the deploy
// by, and the infra→DB migration carries it over verbatim as the apps row key,
// so it names the legacy app whether the deploy stayed stateless or grew into a
// control plane. A control plane created fresh leaves it unset and gets "" back,
// which keeps the header mandatory exactly where no legacy client can exist.
//
// Adding more apps later does not make this ambiguous: their clients are v2 and
// send the header, so a header-less request still belongs to the legacy app.
func LegacyFallbackAppId() string {
	if skip, _ := strconv.ParseBool(GetEnv("SKIP_LEGACY_APP_ID_FALLBACK")); skip {
		return ""
	}
	return strings.TrimSpace(GetEnv("EXPO_APP_ID"))
}

// parseFlatEnvApp reads the v1 single-app env vars and returns an AppConfig.
// Validation is deferred to validateApp so the error path is uniform with the
// JSON case.
func parseFlatEnvApp(appId string) AppConfig {
	app := AppConfig{
		Id:          appId,
		AccessToken: os.Getenv("EXPO_ACCESS_TOKEN"),
	}
	switch os.Getenv("KEYS_STORAGE_TYPE") {
	case "local", "":
		// "local" is the default when unset, matching v1 DefaultEnvValues.
		app.Keys = KeysConfig{
			Mode:        KeysModeLocal,
			PublicPath:  os.Getenv("PUBLIC_LOCAL_EXPO_KEY_PATH"),
			PrivatePath: os.Getenv("PRIVATE_LOCAL_EXPO_KEY_PATH"),
		}
	case "aws-secrets-manager":
		app.Keys = KeysConfig{
			Mode:            KeysModeAWSSM,
			PublicSecretId:  os.Getenv("AWSSM_EXPO_PUBLIC_KEY_SECRET_ID"),
			PrivateSecretId: os.Getenv("AWSSM_EXPO_PRIVATE_KEY_SECRET_ID"),
		}
	case "environment":
		app.Keys = KeysConfig{
			Mode:       KeysModeEnvironment,
			PublicB64:  os.Getenv("PUBLIC_EXPO_KEY_B64"),
			PrivateB64: os.Getenv("PRIVATE_EXPO_KEY_B64"),
		}
	default:
		// Leave Mode empty; validateApp will surface a clear error naming
		// the invalid KEYS_STORAGE_TYPE in the boot log.
	}
	return app
}

func validateApp(app *AppConfig, index int) error {
	prefix := fmt.Sprintf("apps[%d]", index)
	if err := ValidateAppId(app.Id, prefix+".id"); err != nil {
		return err
	}
	if app.AccessToken == "" {
		return fmt.Errorf("%s.accessToken is required", prefix)
	}
	return ValidateKeys(&app.Keys, prefix+".keys")
}

// ValidateAppId centralizes every rule an app id must satisfy before it can
// be used as a path segment, map key, or route parameter. Kept together so
// the constraints stay in sync with isValidAppID / validateSegment.
func ValidateAppId(id, fieldPath string) error {
	if id == "" {
		return fmt.Errorf("%s is required", fieldPath)
	}
	if len(id) > maxAppIdLen {
		return fmt.Errorf("%s %q exceeds max length %d", fieldPath, id, maxAppIdLen)
	}
	// Reserved filesystem names, match validateSegment / isValidAppID so
	// every id-validation path agrees. "." and ".." would resolve to the
	// bucket root (or its parent) when interpolated into {appId}/{branch}/…
	// on the local backend.
	if id == "." || id == ".." {
		return fmt.Errorf("%s %q is reserved", fieldPath, id)
	}
	if _, reserved := reservedAppIds[strings.ToLower(id)]; reserved {
		return fmt.Errorf("%s %q collides with a top-level route name", fieldPath, id)
	}
	for _, r := range id {
		if r == '/' || r == '\\' {
			return fmt.Errorf("%s %q must not contain path separators", fieldPath, id)
		}
		if unicode.IsSpace(r) {
			return fmt.Errorf("%s %q must not contain whitespace", fieldPath, id)
		}
		if unicode.IsControl(r) {
			return fmt.Errorf("%s %q must not contain control characters", fieldPath, id)
		}
		// Only ASCII alphanumerics plus `-` / `_` / `.` are safe across
		// filesystems, URL paths and S3/GCS key rules. Unicode lookalikes
		// (e.g. U+2215 ∕, fullwidth slash U+FF0F ／) would bypass the
		// separator check above while still tripping up downstream consumers.
		if r > unicode.MaxASCII {
			return fmt.Errorf("%s %q must be ASCII", fieldPath, id)
		}
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '-' && r != '_' && r != '.' {
			return fmt.Errorf("%s %q contains invalid character %q", fieldPath, id, r)
		}
	}
	return nil
}

func ValidateKeys(k *KeysConfig, prefix string) error {
	// No config.IsDBMode() check here since to avoid circular depdency
	isDBMode := os.Getenv("DB_URL") != ""
	allowedModes := []string{string(KeysModeLocal), string(KeysModeAWSSM), string(KeysModeEnvironment)}
	if isDBMode {
		allowedModes = append(allowedModes, string(KeysModeDatabase))
	}

	if k.Mode == "" {
		return fmt.Errorf("%s.mode is required (%s)", prefix, strings.Join(allowedModes, "|"))
	}

	hasLocal := k.PublicPath != "" || k.PrivatePath != ""
	hasAWSSM := k.PublicSecretId != "" || k.PrivateSecretId != ""
	hasEnv := k.PublicB64 != "" || k.PrivateB64 != ""

	switch k.Mode {
	case KeysModeLocal:
		if k.PublicPath == "" || k.PrivatePath == "" {
			return fmt.Errorf("%s: mode=local requires publicPath and privatePath", prefix)
		}
		if hasAWSSM || hasEnv {
			return fmt.Errorf("%s: mode=local must not set aws-sm or b64 fields", prefix)
		}
	case KeysModeAWSSM:
		if k.PublicSecretId == "" || k.PrivateSecretId == "" {
			return fmt.Errorf("%s: mode=aws-secrets-manager requires publicSecretId and privateSecretId", prefix)
		}
		if hasLocal || hasEnv {
			return fmt.Errorf("%s: mode=aws-secrets-manager must not set local or b64 fields", prefix)
		}
	case KeysModeEnvironment:
		if k.PublicB64 == "" || k.PrivateB64 == "" {
			return fmt.Errorf("%s: mode=environment requires publicB64 and privateB64", prefix)
		}
		if hasLocal || hasAWSSM {
			return fmt.Errorf("%s: mode=environment must not set local or aws-sm fields", prefix)
		}
		if err := validatePEMKeyB64(k.PublicB64, prefix+".publicB64"); err != nil {
			return err
		}
		if err := validatePEMKeyB64(k.PrivateB64, prefix+".privateB64"); err != nil {
			return err
		}
	case KeysModeDatabase:
		if !isDBMode {
			return fmt.Errorf("%s: mode=database is not allowed when DB_URL is not set", prefix)
		}
		if hasLocal || hasAWSSM {
			return fmt.Errorf("%s: mode=database must not set local or aws-sm fields", prefix)
		}
	default:
		return fmt.Errorf("%s.mode=%q is invalid (expected %s)", prefix, k.Mode, strings.Join(allowedModes, "|"))
	}
	return nil
}

// validatePEMKeyB64 fails fast when a mode=environment key is structurally
// broken: not base64, or base64 that decodes to something that is clearly
// not a PEM-encoded key. Catches two common operator mistakes (double-
// encoded input, pasting raw PEM into a b64 field) at boot instead of at
// first manifest sign, where the symptom is an opaque 500.
func validatePEMKeyB64(b64, fieldPath string) error {
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("%s: invalid base64: %w", fieldPath, err)
	}
	if !strings.Contains(string(decoded), "-----BEGIN ") {
		return fmt.Errorf("%s: decoded value is not a PEM key (missing BEGIN marker)", fieldPath)
	}
	return nil
}

// ListAppIds returns the configured app ids in an unspecified order. Meant
// for read-only introspection (e.g. the dashboard /api/settings endpoint);
// never expose tokens or keys from the same path.
func ListAppIds() []string {
	appsByIdMu.RLock()
	defer appsByIdMu.RUnlock()
	ids := make([]string, 0, len(appsById))
	for id := range appsById {
		ids = append(ids, id)
	}
	return ids
}

// ListApps returns a public-safe descriptor for each configured app. Same
// contract as ListAppIds (unspecified order, snapshot-at-call-time) but
// includes the optional display name so clients like the dashboard can show
// a human-readable label instead of the raw UUID.
func ListApps() []AppDescriptor {
	appsByIdMu.RLock()
	defer appsByIdMu.RUnlock()
	out := make([]AppDescriptor, 0, len(appsById))
	for _, app := range appsById {
		out = append(out, AppDescriptor{Id: app.Id, Name: app.Name})
	}
	return out
}

// GetAppConfig returns the resolved configuration for the given app id.
// Returns an error when the id is unknown so callers can return a clear 404
// instead of silently serving a different app's content.
func GetAppConfig(appId string) (*AppConfig, error) {
	appsByIdMu.RLock()
	defer appsByIdMu.RUnlock()
	app, ok := appsById[appId]
	if !ok {
		return nil, fmt.Errorf("unknown app id %q", appId)
	}
	return app, nil
}

// ResetAppsForTest clears the loaded apps cache. Intended for tests that
// need to reinitialize the config between runs. Unexported-equivalent
// naming keeps it out of "normal" autocompletion.
func ResetAppsForTest() {
	appsByIdMu.Lock()
	appsById = nil
	appsByIdMu.Unlock()
}

// SetAppsForTest replaces the in-memory registry with the given apps. Test-
// only: lets tests exercise the multi-app resolution paths (dashboard tenant
// isolation, per-app routing) directly, without going through the flat-env
// loader, which is single-app. Production multi-app lives in the control
// plane (DB mode); this shortcut just seeds the same registry the bucket
// store reads.
func SetAppsForTest(apps []AppConfig) {
	index := make(map[string]*AppConfig, len(apps))
	for i := range apps {
		app := &apps[i]
		index[app.Id] = app
	}
	appsByIdMu.Lock()
	appsById = index
	appsByIdMu.Unlock()
}
