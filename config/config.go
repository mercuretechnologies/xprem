package config

import (
	"flag"
	"fmt"
	"log"
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

// RepositoryURL is the repository published updates come from, so the dashboard
// can link a commit hash and any #123 in a message. Empty means unconfigured.
func RepositoryURL() string {
	return strings.TrimRight(strings.TrimSpace(GetEnv("EXPO_APP_REPOSITORY_URL")), "/")
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
	// Checked here rather than only in validateApp, which the control plane
	// never reaches: both modes read this var, so both must reject a bad one.
	if repositoryUrl := RepositoryURL(); repositoryUrl != "" && !helpers.IsValidURL(repositoryUrl) {
		log.Fatalf("Invalid EXPO_APP_REPOSITORY_URL: %s", repositoryUrl)
	}
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
