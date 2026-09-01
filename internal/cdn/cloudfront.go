package cdn

import (
	"bytes"
	"crypto"
	"errors"
	"fmt"
	"strings"
	"time"
	"xprem/config"
	"xprem/internal/bucket"
	"xprem/internal/keyStore"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
)

type CloudfrontCDN struct{}

func getCloudfrontDomain() string {
	domain := config.GetEnv("CLOUDFRONT_DOMAIN")
	// The AWS SDK URL signer requires a scheme; tolerate the bare domain
	// shown in the documentation examples.
	if domain != "" && !strings.HasPrefix(domain, "http://") && !strings.HasPrefix(domain, "https://") {
		domain = "https://" + domain
	}
	return domain
}

func getCloudfrontKeyPairId() string {
	return config.GetEnv("CLOUDFRONT_KEY_PAIR_ID")
}

func (c *CloudfrontCDN) isCDNAvailable() bool {
	privateCloudfrontCert := keyStore.GetPrivateCloudfrontKey()
	domain := getCloudfrontDomain()
	keyPairId := getCloudfrontKeyPairId()
	return privateCloudfrontCert != "" && domain != "" && keyPairId != ""
}

func getSigner(key string) (crypto.Signer, error) {
	privateKey, err := sign.LoadPEMPrivKeyPKCS8AsSigner(bytes.NewReader([]byte(key)))
	if err != nil {
		// A fresh reader is required: the failed PKCS#8 attempt above has
		// already consumed the previous one, so reusing it would make this
		// fallback always fail with "no valid PEM data provided".
		privateKey, err = sign.LoadPEMPrivKey(bytes.NewReader([]byte(key)))
		if err != nil {
			return nil, fmt.Errorf("error parsing private key: %w", err)
		}
	}
	return privateKey, nil
}

func (c *CloudfrontCDN) ComputeRedirectionURLForAsset(appId, branch, runtimeVersion, updateId, asset string) (string, error) {
	// Must match the v2 bucket layout exactly, if the CloudFront origin is
	// an S3 bucket, the object sits at {BUCKET_KEY_PREFIX}{appId}/{branch}/…
	// Operators using BUCKET_KEY_PREFIX must NOT also configure a
	// CloudFront Origin Path equal to the prefix; the path is part of the
	// signed resource and would be applied twice.
	endpoint := bucket.ResolveKeyPrefix() + fmt.Sprintf("%s/%s/%s/%s/%s", appId, branch, runtimeVersion, updateId, asset)
	return c.signObjectKey(endpoint)
}

func (c *CloudfrontCDN) ComputeRedirectionURLForBlob(appId, hash string) (string, error) {
	return c.signObjectKey(bucket.ResolveKeyPrefix() + bucket.BlobObjectKey(appId, hash))
}

func (c *CloudfrontCDN) signObjectKey(endpoint string) (string, error) {
	domain := getCloudfrontDomain()
	keyPairId := getCloudfrontKeyPairId()
	privateCloudfrontCert := keyStore.GetPrivateCloudfrontKey()

	if domain == "" || keyPairId == "" || privateCloudfrontCert == "" {
		return "", errors.New("CloudFront configuration is incomplete")
	}

	privateKey, err := getSigner(privateCloudfrontCert)
	if err != nil {
		return "", fmt.Errorf("error parsing private key: %w", err)
	}

	resource := fmt.Sprintf("%s/%s", domain, endpoint)
	policy := sign.NewCannedPolicy(resource, time.Now().Add(10*time.Minute))
	signer := sign.NewURLSigner(keyPairId, privateKey)
	return signer.SignWithPolicy(resource, policy)
}
