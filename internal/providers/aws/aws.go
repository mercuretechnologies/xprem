package aws

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
	"xprem/config"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

var (
	s3Client     *s3.Client
	s3ClientErr  error
	initS3Client sync.Once
)

func GetS3Client() (*s3.Client, error) {
	initS3Client.Do(func() {
		var cfg awssdk.Config
		opts := []func(*awsconfig.LoadOptions) error{
			awsconfig.WithRegion(config.GetEnv("AWS_REGION")),
		}
		accessKey := config.GetEnv("AWS_ACCESS_KEY_ID")
		secretKey := config.GetEnv("AWS_SECRET_ACCESS_KEY")
		if accessKey != "" && secretKey != "" {
			opts = append(opts, awsconfig.WithCredentialsProvider(
				awssdk.CredentialsProviderFunc(func(ctx context.Context) (awssdk.Credentials, error) {
					return awssdk.Credentials{
						AccessKeyID:     accessKey,
						SecretAccessKey: secretKey,
					}, nil
				}),
			))
		}

		cfg, s3ClientErr = awsconfig.LoadDefaultConfig(context.TODO(), opts...)
		if s3ClientErr != nil {
			s3ClientErr = fmt.Errorf("error loading AWS configuration: %w", s3ClientErr)
			return
		}

		s3Client = s3.NewFromConfig(cfg, applyS3ClientOptions)
	})

	return s3Client, s3ClientErr
}

func applyS3ClientOptions(o *s3.Options) {
	baseEndpoint := config.GetEnv("AWS_BASE_ENDPOINT")
	if baseEndpoint != "" {
		o.BaseEndpoint = awssdk.String(baseEndpoint)
	}

	o.UsePathStyle = config.GetEnv("AWS_S3_FORCE_PATH_STYLE") == "true"
}

// Process-local only: secret values must never reach the shared cache. The
// TTL bounds how long a rotated secret keeps being served.
var (
	secretCache   sync.Map
	secretCacheMu sync.Mutex
)

const secretCacheTTL = 5 * time.Minute

type cachedSecret struct {
	value     string
	fetchedAt time.Time
}

// FetchSecret reads one Secrets Manager secret, memoized for secretCacheTTL:
// signing paths call it per request.
func FetchSecret(secretName string) string {
	if entry, ok := secretCache.Load(secretName); ok {
		if cached := entry.(cachedSecret); time.Since(cached.fetchedAt) < secretCacheTTL {
			return cached.value
		}
	}
	// One flight per expired secret; concurrent callers wait and reuse it.
	secretCacheMu.Lock()
	defer secretCacheMu.Unlock()
	if entry, ok := secretCache.Load(secretName); ok {
		if cached := entry.(cachedSecret); time.Since(cached.fetchedAt) < secretCacheTTL {
			return cached.value
		}
	}

	cfg, err := awsconfig.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("Failed to load AWS configuration: %v", err)
	}

	client := secretsmanager.NewFromConfig(cfg)

	resp, err := client.GetSecretValue(context.TODO(), &secretsmanager.GetSecretValueInput{
		SecretId: awssdk.String(secretName),
	})
	if err != nil {
		log.Fatalf("Failed to retrieve secret %s: %v", secretName, err)
	}

	if resp.SecretString == nil {
		log.Fatalf("Secret %s has no SecretString", secretName)
	}

	secretCache.Store(secretName, cachedSecret{value: *resp.SecretString, fetchedAt: time.Now()})
	return *resp.SecretString
}
