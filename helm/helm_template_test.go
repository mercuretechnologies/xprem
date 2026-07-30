package helm

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRedisSentinelModeRendersRedisAuthAndTLSEnvVars(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}

	cmd := exec.Command(
		"helm",
		"template",
		"xprem",
		".",
		"--set-string",
		"cacheMode=redis-sentinel",
		"--set-string",
		"useRedisTLS=true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}

	env := deploymentEnvByName(t, out)

	for _, name := range []string{
		"REDIS_SENTINEL_ADDRS",
		"REDIS_SENTINEL_MASTER_NAME",
		"REDIS_USE_TLS",
		"REDIS_PASSWORD",
		"REDIS_USERNAME",
		"REDIS_CA_CERT_B64",
	} {
		if _, ok := env[name]; !ok {
			t.Fatalf("expected %s to be rendered in redis-sentinel mode", name)
		}
	}

	for _, name := range []string{
		"REDIS_SENTINEL_MASTER_NAME",
		"REDIS_PASSWORD",
		"REDIS_USERNAME",
		"REDIS_CA_CERT_B64",
	} {
		if !secretKeyRefOptional(env[name]) {
			t.Fatalf("expected %s to be rendered as an optional secret key ref", name)
		}
	}

	for _, name := range []string{"REDIS_HOST", "REDIS_PORT"} {
		if _, ok := env[name]; ok {
			t.Fatalf("did not expect %s to be rendered in redis-sentinel mode", name)
		}
	}
}

// A control-plane deploy upgrading from v2 still needs the flat single-app
// vars: the legacy import reads them to migrate the app into the database and
// EXPO_APP_ID keeps serving v1 clients that send no expo-app-id header. They
// must therefore render (as optional) even when controlPlane=true.
func TestControlPlaneModeStillRendersLegacyFlatEnvVars(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}

	cmd := exec.Command(
		"helm",
		"template",
		"xprem",
		".",
		"--set-string",
		"controlPlane=true",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}

	env := deploymentEnvByName(t, out)

	for _, name := range []string{
		"EXPO_APP_ID",
		"EXPO_ACCESS_TOKEN",
		"SKIP_LEGACY_APP_ID_FALLBACK",
		// Default keysStorageType is aws-secrets-manager, so the matching
		// key vars must be offered to the legacy import too.
		"AWSSM_EXPO_PUBLIC_KEY_SECRET_ID",
		"AWSSM_EXPO_PRIVATE_KEY_SECRET_ID",
		"KEYS_STORAGE_TYPE",
		"DB_URL",
	} {
		if _, ok := env[name]; !ok {
			t.Fatalf("expected %s to be rendered in control-plane mode", name)
		}
	}

	for _, name := range []string{
		"EXPO_APP_ID",
		"EXPO_ACCESS_TOKEN",
		"SKIP_LEGACY_APP_ID_FALLBACK",
		"AWSSM_EXPO_PUBLIC_KEY_SECRET_ID",
		"AWSSM_EXPO_PRIVATE_KEY_SECRET_ID",
	} {
		if !secretKeyRefOptional(env[name]) {
			t.Fatalf("expected %s to be optional in control-plane mode", name)
		}
	}

	if secretKeyRefOptional(env["DB_URL"]) {
		t.Fatal("expected DB_URL to be required in control-plane mode")
	}

	for _, name := range []string{"DB_MAX_CONN_LIFETIME", "DB_MAX_CONN_IDLE_TIME"} {
		if !secretKeyRefOptional(env[name]) {
			t.Fatalf("expected %s to be an optional secret key ref in control-plane mode", name)
		}
	}

	// Key vars of the non-selected storage modes stay hidden.
	for _, name := range []string{
		"PUBLIC_LOCAL_EXPO_KEY_PATH",
		"PRIVATE_LOCAL_EXPO_KEY_PATH",
		"PUBLIC_EXPO_KEY_B64",
		"PRIVATE_EXPO_KEY_B64",
	} {
		if _, ok := env[name]; ok {
			t.Fatalf("did not expect %s with keysStorageType=aws-secrets-manager", name)
		}
	}
}

func TestStatelessModeRequiresFlatEnvVars(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}

	cmd := exec.Command("helm", "template", "xprem", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}

	env := deploymentEnvByName(t, out)

	for _, name := range []string{"EXPO_APP_ID", "EXPO_ACCESS_TOKEN"} {
		if _, ok := env[name]; !ok {
			t.Fatalf("expected %s to be rendered in stateless mode", name)
		}
		if secretKeyRefOptional(env[name]) {
			t.Fatalf("expected %s to be required in stateless mode", name)
		}
	}
}

// Optional tuning vars carry `required: false` plus `enabled: true`; without
// the latter the template renders nothing at all, which is how these vars
// silently disappeared from deployments in the past.
func TestOptionalTuningVarsAreRendered(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}

	cmd := exec.Command("helm", "template", "xprem", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}

	env := deploymentEnvByName(t, out)

	for _, name := range []string{
		"BUCKET_KEY_PREFIX",
		"S3_KEY_PREFIX",
		"AWS_BASE_ENDPOINT",
		"AWS_S3_FORCE_PATH_STYLE",
		"SKIP_LEGACY_APP_ID_FALLBACK",
		"CACHE_KEY_PREFIX",
		"DISABLE_S3_DIRECT_CDN",
		"BUCKET_MIGRATION_CONCURRENCY",
		"CLICKHOUSE_URL",
		"GEOIP_MMDB_PATH",
		"DISABLE_DEVICE_TELEMETRY",
	} {
		if !secretKeyRefOptional(env[name]) {
			t.Fatalf("expected %s to be rendered as an optional secret key ref", name)
		}
	}

	// The pool tuning pair only exists in control-plane mode.
	for _, name := range []string{"DB_MAX_CONN_LIFETIME", "DB_MAX_CONN_IDLE_TIME"} {
		if _, ok := env[name]; ok {
			t.Fatalf("did not expect %s with controlPlane=false", name)
		}
	}
}

// The generic CDN pair only renders when the toggle is on, and both keys stay
// optional so a secret holding only the deprecated S3_CDN_PREFIX keeps working
// after an upgrade.
func TestGenericCDNVarsRenderOptionalUnderToggle(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}

	cmd := exec.Command("helm", "template", "xprem", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	env := deploymentEnvByName(t, out)
	for _, name := range []string{"CDN_BASE_URL", "S3_CDN_PREFIX"} {
		if _, ok := env[name]; ok {
			t.Fatalf("expected %s not to be rendered with useGenericCDN=false", name)
		}
	}

	cmd = exec.Command("helm", "template", "xprem", ".", "--set", "useGenericCDN=true")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	env = deploymentEnvByName(t, out)
	for _, name := range []string{"CDN_BASE_URL", "S3_CDN_PREFIX"} {
		if !secretKeyRefOptional(env[name]) {
			t.Fatalf("expected %s to be rendered as an optional secret key ref", name)
		}
	}
}

// Azure storage vars render only when storageMode=azure: the three required
// ones as non-optional secret key refs, the endpoint override as optional.
func TestAzureStorageVarsRenderUnderAzureMode(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}

	cmd := exec.Command("helm", "template", "xprem", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	env := deploymentEnvByName(t, out)
	for _, name := range []string{"AZURE_BLOB_CONTAINER_NAME", "AZURE_STORAGE_ACCOUNT_NAME", "AZURE_STORAGE_ACCOUNT_KEY", "AZURE_BLOB_ENDPOINT"} {
		if _, ok := env[name]; ok {
			t.Fatalf("expected %s not to be rendered with the default storage mode", name)
		}
	}

	cmd = exec.Command("helm", "template", "xprem", ".", "--set", "storageMode=azure")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	env = deploymentEnvByName(t, out)
	for _, name := range []string{"AZURE_BLOB_CONTAINER_NAME", "AZURE_STORAGE_ACCOUNT_NAME", "AZURE_STORAGE_ACCOUNT_KEY"} {
		ref, ok := env[name]
		if !ok {
			t.Fatalf("expected %s to be rendered in azure mode", name)
		}
		if secretKeyRefOptional(ref) {
			t.Fatalf("expected %s to be a required secret key ref in azure mode", name)
		}
	}
	if _, ok := env["AZURE_BLOB_ENDPOINT"]; !ok {
		t.Fatal("expected AZURE_BLOB_ENDPOINT to be rendered in azure mode")
	}
	if !secretKeyRefOptional(env["AZURE_BLOB_ENDPOINT"]) {
		t.Fatal("expected AZURE_BLOB_ENDPOINT to be an optional secret key ref")
	}
}

// Liveness must stay on /hc (green during long bucket migrations) while
// readiness moves to /ready (red until the migrations are done), otherwise
// the orchestrator either kills migrating pods or routes traffic to them.
func TestProbePaths(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is not installed")
	}

	cmd := exec.Command("helm", "template", "xprem", ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}

	container := deploymentContainer(t, out)
	liveness := asMap(t, asMap(t, container["livenessProbe"])["httpGet"])
	readiness := asMap(t, asMap(t, container["readinessProbe"])["httpGet"])
	if liveness["path"] != "/hc" {
		t.Fatalf("expected livenessProbe on /hc, got %v", liveness["path"])
	}
	if readiness["path"] != "/ready" {
		t.Fatalf("expected readinessProbe on /ready, got %v", readiness["path"])
	}
}

func deploymentContainer(t *testing.T, manifest []byte) map[string]any {
	t.Helper()

	decoder := yaml.NewDecoder(bytes.NewReader(manifest))
	for {
		var doc map[string]any
		err := decoder.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("failed to decode manifest: %v", err)
		}
		if doc["kind"] != "Deployment" {
			continue
		}
		containers := asSlice(t, asMap(t, asMap(t, asMap(t, doc["spec"])["template"])["spec"])["containers"])
		return asMap(t, containers[0])
	}

	t.Fatal("deployment manifest not found")
	return nil
}

func deploymentEnvByName(t *testing.T, manifest []byte) map[string]map[string]any {
	t.Helper()

	container := deploymentContainer(t, manifest)
	envList := asSlice(t, container["env"])
	envByName := make(map[string]map[string]any, len(envList))
	for _, envItem := range envList {
		envMap := asMap(t, envItem)
		name, ok := envMap["name"].(string)
		if !ok {
			t.Fatalf("env item is missing string name: %#v", envMap)
		}
		envByName[name] = envMap
	}
	return envByName
}

func secretKeyRefOptional(env map[string]any) bool {
	valueFrom, ok := env["valueFrom"].(map[string]any)
	if !ok {
		return false
	}
	secretKeyRef, ok := valueFrom["secretKeyRef"].(map[string]any)
	if !ok {
		return false
	}
	optional, ok := secretKeyRef["optional"].(bool)
	return ok && optional
}

func asMap(t *testing.T, value any) map[string]any {
	t.Helper()

	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", value)
	}
	return result
}

func asSlice(t *testing.T, value any) []any {
	t.Helper()

	result, ok := value.([]any)
	if !ok {
		t.Fatalf("expected slice, got %T", value)
	}
	return result
}
