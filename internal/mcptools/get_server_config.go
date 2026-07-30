package mcptools

import (
	"context"
	"errors"
	"expo-open-ota/config"
	"expo-open-ota/internal/cdn"
	"expo-open-ota/internal/helpers"
	"expo-open-ota/internal/version"

	mcpprot "github.com/modelcontextprotocol/go-sdk/mcp"
)

// GetServerConfigOutput mirrors what /api/settings shows every member, same
// masking included. Two dashboard fields are deliberately absent: APPS
// (get_apps already answers it) and EXPO_ACCOUNT_USERNAME (stateless only,
// and MCP requires the control plane).
type GetServerConfigOutput struct {
	BaseUrl                           string `json:"baseUrl"`
	ServerVersion                     string `json:"serverVersion"`
	ControlPlaneEnabled               bool   `json:"controlPlaneEnabled"`
	CacheMode                         string `json:"cacheMode,omitempty"`
	RedisHost                         string `json:"redisHost,omitempty"`
	RedisPort                         string `json:"redisPort,omitempty"`
	RedisSentinelAddrs                string `json:"redisSentinelAddrs,omitempty"`
	RedisSentinelMasterName           string `json:"redisSentinelMasterName,omitempty"`
	StorageMode                       string `json:"storageMode"`
	S3BucketName                      string `json:"s3BucketName,omitempty"`
	GcsBucketName                     string `json:"gcsBucketName,omitempty"`
	AzureBlobContainerName            string `json:"azureBlobContainerName,omitempty"`
	AzureStorageAccountName           string `json:"azureStorageAccountName,omitempty"`
	LocalBucketBasePath               string `json:"localBucketBasePath,omitempty"`
	AwsRegion                         string `json:"awsRegion,omitempty"`
	AwsBaseEndpoint                   string `json:"awsBaseEndpoint,omitempty"`
	AwsS3ForcePathStyle               string `json:"awsS3ForcePathStyle,omitempty"`
	AwsAccessKeyId                    string `json:"awsAccessKeyId,omitempty"`
	CloudfrontDomain                  string `json:"cloudfrontDomain,omitempty"`
	CloudfrontKeyPairId               string `json:"cloudfrontKeyPairId,omitempty"`
	PrivateCloudfrontKeyB64           string `json:"privateCloudfrontKeyB64,omitempty"`
	AwssmCloudfrontPrivateKeySecretId string `json:"awssmCloudfrontPrivateKeySecretId,omitempty"`
	PrivateCloudfrontKeyPath          string `json:"privateCloudfrontKeyPath,omitempty"`
	PrometheusEnabled                 string `json:"prometheusEnabled,omitempty"`
	// CdnType is the CDN the server actually resolved at boot, not the raw
	// variable.
	CdnType    string `json:"cdnType,omitempty"`
	CdnBaseUrl string `json:"cdnBaseUrl,omitempty"`
	SsoEnabled bool   `json:"ssoEnabled"`
}

func getServerConfigHandler(deps Deps) func(ctx context.Context, req *mcpprot.CallToolRequest, _ struct{}) (*mcpprot.CallToolResult, GetServerConfigOutput, error) {
	return func(ctx context.Context, req *mcpprot.CallToolRequest, _ struct{}) (*mcpprot.CallToolResult, GetServerConfigOutput, error) {
		principal := PrincipalFromRequest(req)
		if principal == nil {
			return nil, GetServerConfigOutput{}, errors.New("no authenticated account on this session")
		}
		return nil, GetServerConfigOutput{
			BaseUrl:                           config.GetEnv("BASE_URL"),
			ServerVersion:                     version.Version,
			ControlPlaneEnabled:               config.IsDBMode(),
			CacheMode:                         config.GetEnv("CACHE_MODE"),
			RedisHost:                         config.GetEnv("REDIS_HOST"),
			RedisPort:                         config.GetEnv("REDIS_PORT"),
			RedisSentinelAddrs:                config.GetEnv("REDIS_SENTINEL_ADDRS"),
			RedisSentinelMasterName:           config.GetEnv("REDIS_SENTINEL_MASTER_NAME"),
			StorageMode:                       config.GetEnv("STORAGE_MODE"),
			S3BucketName:                      config.GetEnv("S3_BUCKET_NAME"),
			GcsBucketName:                     config.GetEnv("GCS_BUCKET_NAME"),
			AzureBlobContainerName:            config.GetEnv("AZURE_BLOB_CONTAINER_NAME"),
			AzureStorageAccountName:           config.GetEnv("AZURE_STORAGE_ACCOUNT_NAME"),
			LocalBucketBasePath:               config.GetEnv("LOCAL_BUCKET_BASE_PATH"),
			AwsRegion:                         config.GetEnv("AWS_REGION"),
			AwsBaseEndpoint:                   config.GetEnv("AWS_BASE_ENDPOINT"),
			AwsS3ForcePathStyle:               config.GetEnv("AWS_S3_FORCE_PATH_STYLE"),
			AwsAccessKeyId:                    helpers.MaskSecret(config.GetEnv("AWS_ACCESS_KEY_ID")),
			CloudfrontDomain:                  config.GetEnv("CLOUDFRONT_DOMAIN"),
			CloudfrontKeyPairId:               helpers.MaskSecret(config.GetEnv("CLOUDFRONT_KEY_PAIR_ID")),
			PrivateCloudfrontKeyB64:           helpers.MaskSecret(config.GetEnv("PRIVATE_CLOUDFRONT_KEY_B64")),
			AwssmCloudfrontPrivateKeySecretId: config.GetEnv("AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID"),
			PrivateCloudfrontKeyPath:          config.GetEnv("PRIVATE_CLOUDFRONT_KEY_PATH"),
			PrometheusEnabled:                 config.GetEnv("PROMETHEUS_ENABLED"),
			CdnType:                           cdn.ResolvedType(),
			CdnBaseUrl:                        cdn.ResolveCDNBaseURL(),
			SsoEnabled:                        deps.SSOEnabled(ctx),
		}, nil
	}
}

func registerGetServerConfig(server *mcpprot.Server, deps Deps) {
	mcpprot.AddTool(server, &mcpprot.Tool{
		Name:        "get_server_config",
		Description: "This deployment's configuration, as the dashboard settings page shows it: base URL, server version, control plane, cache/redis, storage and buckets, CDN (resolved type and base URL), Prometheus, SSO. Key-like values are masked. Use get_apps for the app list.",
		Annotations: &mcpprot.ToolAnnotations{Title: "Server configuration", ReadOnlyHint: true},
	}, getServerConfigHandler(deps))
}
