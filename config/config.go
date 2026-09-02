package config

import (
	"flag"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
	"xprem/internal/helpers"

	"github.com/joho/godotenv"
)

func validateStorageMode(storageMode string) bool {
	return storageMode == "local" || storageMode == "s3" || storageMode == "gcs" || storageMode == "azure"
}

func GetPort() string {
	port := GetEnv("PORT")
	if port == "" {
		port = "3000"
	}
	return port
}

func GetDBURL() string {
	return GetEnv("DB_URL")
}

func IsDBMode() bool {
	return GetDBURL() != ""
}

// GetClickHouseURL returns the ClickHouse DSN the Observe feature persists
// telemetry through (e.g. clickhouse://user:password@host:9000/database).
// Empty means Observe (identity included) is not enabled.
func GetClickHouseURL() string {
	return GetEnv("CLICKHOUSE_URL")
}

// IsDeviceTelemetryDisabled reports whether the operator turned off every
// recording made about a device: the manifest check-ins that populate the
// Postgres registry (device_identity and the update-health tables it feeds),
// the identity ops and the telemetry rows the Observe SDK dispatches, and the
// ClickHouse connection those rows would land in. It is the privacy switch a
// deployment flips when it wants to serve updates and collect nothing about
// who receives them.
//
// It disables COLLECTION, not the schema: the tables stay, and whatever was
// recorded before the switch stays with them. Deleting it is the operator's
// call, not a boot side effect.
func IsDeviceTelemetryDisabled() bool {
	disabled, _ := strconv.ParseBool(GetEnv("DISABLE_DEVICE_TELEMETRY"))
	return disabled
}

func IsServerTelemetryDisabled() bool {
	disabled, _ := strconv.ParseBool(GetEnv("DISABLE_TELEMETRY"))
	return disabled
}

func ValidateMasterKey() error {
	awsKeyId := GetEnv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID")
	localKey := GetEnv("DB_KEYS_MASTER_KEY_B64")
	if awsKeyId == "" && localKey == "" {
		return fmt.Errorf("Neither AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID nor DB_KEYS_MASTER_KEY_B64 is set: DB mode requires a master key to seal the per-app Expo signing keys stored in Postgres. Generate one with `openssl rand -base64 32`")
	}
	if awsKeyId != "" && localKey != "" {
		log.Printf("Both AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID and DB_KEYS_MASTER_KEY_B64 are set; please set only one")
		return fmt.Errorf("Both AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID and DB_KEYS_MASTER_KEY_B64 are set; please set only one")
	}
	return nil
}

func validateBucketParams(storageMode string) bool {
	switch storageMode {
	case "s3":
		bucketName := GetEnv("S3_BUCKET_NAME")
		if bucketName == "" {
			log.Printf("S3_BUCKET_NAME not set")
			return false
		}
		region := GetEnv("AWS_REGION")
		if region == "" {
			log.Printf("AWS_REGION not set")
			return false
		}
	case "gcs":
		bucketName := GetEnv("GCS_BUCKET_NAME")
		if bucketName == "" {
			log.Printf("GCS_BUCKET_NAME not set")
			return false
		}
	case "azure":
		if GetEnv("AZURE_BLOB_CONTAINER_NAME") == "" {
			log.Printf("AZURE_BLOB_CONTAINER_NAME not set")
			return false
		}
		if GetEnv("AZURE_STORAGE_ACCOUNT_NAME") == "" {
			log.Printf("AZURE_STORAGE_ACCOUNT_NAME not set")
			return false
		}
		// The account key is required in every case: shared key auth is the
		// only supported mode and SAS URLs cannot be signed without it.
		if GetEnv("AZURE_STORAGE_ACCOUNT_KEY") == "" {
			log.Printf("AZURE_STORAGE_ACCOUNT_KEY not set")
			return false
		}
	case "local":
		// Already handled by default values
		return true
	default:
		return false
	}
	return true
}

func validateBaseUrl(baseUrl string) bool {
	if baseUrl == "" || !helpers.IsValidURL(baseUrl) {
		return false
	}
	parsed, err := url.Parse(baseUrl)
	if err != nil {
		return false
	}
	// Paths are appended to BASE_URL, so a query or fragment would end up
	// before them.
	return parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == ""
}

// BaseURL is BASE_URL without any trailing slash.
func BaseURL() string {
	return strings.TrimRight(GetEnv("BASE_URL"), "/")
}

// ServeFromSubPath reports whether the server itself serves under BASE_URL's
// path; when false the reverse proxy is expected to strip the prefix.
func ServeFromSubPath() bool {
	return GetEnv("SERVE_FROM_SUB_PATH") == "true"
}

// PublicPath is the path component of BASE_URL, empty when BASE_URL has none.
func PublicPath() string {
	parsed, err := url.Parse(BaseURL())
	if err != nil {
		return ""
	}
	p := strings.TrimRight(parsed.Path, "/")
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

// PublicHref is path as a root-relative URL under BASE_URL.
func PublicHref(path string) string {
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return PublicPath() + path
}

func IsTestMode() bool {
	return flag.Lookup("test.v") != nil
}

func resolveDefaultBaseUrl() string {
	port := os.Getenv("PORT")
	if port == "" {
		return "http://localhost:3000"
	}
	return "http://localhost:" + port
}

func LoadConfig() {
	err := godotenv.Load()
	if err != nil {
		log.Printf("No .env file found, continuing with runtime environment variables.")
	}
	storageMode := GetEnv("STORAGE_MODE")
	if !validateStorageMode(storageMode) {
		log.Fatalf("Invalid STORAGE_MODE: %s", storageMode)
	}
	bucketParamsValid := validateBucketParams(storageMode)
	if !bucketParamsValid {
		log.Fatalf("Invalid bucket parameters")
	}
	baseUrl := GetEnv("BASE_URL")
	if !validateBaseUrl(baseUrl) {
		log.Fatalf("Invalid BASE_URL: %s", baseUrl)
	}
	jwtSecret := GetEnv("JWT_SECRET")
	if jwtSecret == "" {
		log.Fatalf("JWT_SECRET not set")
	}
	if _, err := parseBundleDiffingMaxBundleSize(); err != nil {
		log.Fatalf("Invalid BUNDLE_DIFFING_MAX_BUNDLE_SIZE_MB: %v", err)
	}
	if _, err := parseBundleDiffingPatchMaxRatio(); err != nil {
		log.Fatalf("Invalid BUNDLE_DIFFING_PATCH_MAX_RATIO: %v", err)
	}
}

// IsBundleDiffingEnabled reports whether bundle patches are computed at
// publish and served to devices (BUNDLE_DIFFING=true, off by default).
func IsBundleDiffingEnabled() bool {
	enabled, _ := strconv.ParseBool(GetEnv("BUNDLE_DIFFING"))
	return enabled && IsDBMode()
}

// IsBundleDiffingCDNRedirect reports whether patch requests are redirected to
// the CDN instead of served by this server (BUNDLE_DIFFING_CDN_REDIRECT=true).
// The operator asserts that the CDN edge adds the im and expo-base-update-id
// response headers on the bsDiff objects; nothing here can check it.
func IsBundleDiffingCDNRedirect() bool {
	enabled, _ := strconv.ParseBool(GetEnv("BUNDLE_DIFFING_CDN_REDIRECT"))
	return enabled
}

// BundleDiffingMaxBundleSize is the largest launch asset, in bytes, a patch
// job loads in memory (BUNDLE_DIFFING_MAX_BUNDLE_SIZE_MB, default 128).
func BundleDiffingMaxBundleSize() int64 {
	size, err := parseBundleDiffingMaxBundleSize()
	if err != nil {
		size, _ = parseMB(DefaultEnvValues["BUNDLE_DIFFING_MAX_BUNDLE_SIZE_MB"])
	}
	return size
}

// BundleDiffingPatchMaxRatio is the largest patch worth storing, as a fraction
// of the gzipped bundle a full download would cost
// (BUNDLE_DIFFING_PATCH_MAX_RATIO in (0, 1], default 0.3).
func BundleDiffingPatchMaxRatio() float64 {
	ratio, err := parseBundleDiffingPatchMaxRatio()
	if err != nil {
		ratio, _ = parseRatio(DefaultEnvValues["BUNDLE_DIFFING_PATCH_MAX_RATIO"])
	}
	return ratio
}

func parseBundleDiffingMaxBundleSize() (int64, error) {
	return parseMB(GetEnv("BUNDLE_DIFFING_MAX_BUNDLE_SIZE_MB"))
}

func parseBundleDiffingPatchMaxRatio() (float64, error) {
	return parseRatio(GetEnv("BUNDLE_DIFFING_PATCH_MAX_RATIO"))
}

func parseMB(value string) (int64, error) {
	mb, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, err
	}
	if mb <= 0 || mb > math.MaxInt64>>20 {
		return 0, fmt.Errorf("%d is not a usable number of megabytes", mb)
	}
	return mb << 20, nil
}

func parseRatio(value string) (float64, error) {
	ratio, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(ratio) || ratio <= 0 || ratio > 1 {
		return 0, fmt.Errorf("%v is not in (0, 1]", ratio)
	}
	return ratio, nil
}

var DefaultEnvValues = map[string]string{
	"LOCAL_BUCKET_BASE_PATH": "./updates",
	"STORAGE_MODE":           "local",
	"BASE_URL":               resolveDefaultBaseUrl(),
	"JWT_SECRET":             "",
	"AWS_REGION":             "eu-west-3",
	"AWS_BASE_ENDPOINT":      "",

	// Audit archive (ee/audit): opt-in periodic NDJSON export of the audit
	// log to a DEDICATED bucket/container/directory (per-provider name
	// variables, see bucket.GetAuditLogsObjectStore). Off by default:
	// writing to the operator's storage must be a choice.
	"ARCHIVE_AUDIT_LOGS":                 "false",
	"AUDIT_LOGS_EXPORT_INTERVAL_SECONDS": "300",
	"LOCAL_AUDIT_LOGS_BASE_PATH":         "./audit-logs",

	// Audit log retention (ee/audit): about 1.5 years, matching EAS and the
	// 1-3 year industry norm. Purged rows are gone from Postgres; anything
	// longer lived belongs to the operator's own pipeline (database backups,
	// the audit stream once enabled).
	"AUDIT_LOG_RETENTION_DAYS": "550",

	// Bundle diffing (bsdiff patches between updates): off by default. Patches
	// are served by this server, not the CDN, so a patch is only kept when it
	// beats the gzipped bundle by a wide margin.
	"BUNDLE_DIFFING":                    "false",
	"BUNDLE_DIFFING_CDN_REDIRECT":       "false",
	"BUNDLE_DIFFING_MAX_BUNDLE_SIZE_MB": "128",
	"BUNDLE_DIFFING_PATCH_MAX_RATIO":    "0.3",

	// Database connection defaults
	"DB_URL":                "",
	"DB_MAX_CONNS":          "25",
	"DB_MIN_CONNS":          "5",
	"DB_MAX_CONN_LIFETIME":  "30m",
	"DB_MAX_CONN_IDLE_TIME": "5m",
}

func GetEnv(key string) string {
	value := os.Getenv(key)
	if value == "" {
		defaultValue := DefaultEnvValues[key]
		if defaultValue != "" {
			return defaultValue
		}
		return ""
	}
	return value
}
