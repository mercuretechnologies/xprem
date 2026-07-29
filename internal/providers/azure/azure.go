package azure

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"xprem/config"

	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/sas"
)

var (
	azClient     *azblob.Client
	azClientErr  error
	initAzClient sync.Once
)

// ResolveServiceURL returns the blob service endpoint: AZURE_BLOB_ENDPOINT
// when set (Azurite, private endpoints), otherwise the standard public
// endpoint derived from the account name.
func ResolveServiceURL() string {
	if endpoint := config.GetEnv("AZURE_BLOB_ENDPOINT"); endpoint != "" {
		return strings.TrimSuffix(endpoint, "/")
	}
	accountName := config.GetEnv("AZURE_STORAGE_ACCOUNT_NAME")
	if accountName == "" {
		return ""
	}
	return fmt.Sprintf("https://%s.blob.core.windows.net", accountName)
}

func sharedKeyCredential() (*azblob.SharedKeyCredential, error) {
	accountName := config.GetEnv("AZURE_STORAGE_ACCOUNT_NAME")
	accountKey := config.GetEnv("AZURE_STORAGE_ACCOUNT_KEY")
	if accountName == "" || accountKey == "" {
		return nil, errors.New("AZURE_STORAGE_ACCOUNT_NAME or AZURE_STORAGE_ACCOUNT_KEY not set")
	}
	return azblob.NewSharedKeyCredential(accountName, accountKey)
}

func GetClient() (*azblob.Client, error) {
	initAzClient.Do(func() {
		cred, err := sharedKeyCredential()
		if err != nil {
			azClientErr = err
			return
		}
		serviceURL := ResolveServiceURL()
		if serviceURL == "" {
			azClientErr = errors.New("unable to resolve the Azure blob service URL")
			return
		}
		azClient, azClientErr = azblob.NewClientWithSharedKeyCredential(serviceURL, cred, nil)
		if azClientErr != nil {
			azClientErr = fmt.Errorf("error initializing Azure blob client: %w", azClientErr)
		}
	})
	return azClient, azClientErr
}

// SignBlobSAS returns a full SAS URL for a single blob. Signing is pure HMAC
// with the account key, no network call involved. The start time sits in the
// past to tolerate clock skew between this server and Azure.
func SignBlobSAS(containerName, blobPath string, permissions sas.BlobPermissions, expiry time.Duration) (string, error) {
	cred, err := sharedKeyCredential()
	if err != nil {
		return "", err
	}
	serviceURL := ResolveServiceURL()
	if serviceURL == "" {
		return "", errors.New("unable to resolve the Azure blob service URL")
	}
	protocol := sas.ProtocolHTTPS
	if strings.HasPrefix(serviceURL, "http://") {
		// Azurite and other local emulators serve plain HTTP.
		protocol = sas.ProtocolHTTPSandHTTP
	}
	now := time.Now().UTC()
	values := sas.BlobSignatureValues{
		Protocol:      protocol,
		StartTime:     now.Add(-5 * time.Minute),
		ExpiryTime:    now.Add(expiry),
		Permissions:   permissions.String(),
		ContainerName: containerName,
		BlobName:      blobPath,
	}
	queryParams, err := values.SignWithSharedKey(cred)
	if err != nil {
		return "", fmt.Errorf("error signing SAS: %w", err)
	}
	return fmt.Sprintf("%s/%s/%s?%s", serviceURL, containerName, escapeBlobPath(blobPath), queryParams.Encode()), nil
}

// escapeBlobPath escapes each path segment while keeping the "/" separators,
// matching how the SDK clients build blob URLs.
func escapeBlobPath(blobPath string) string {
	segments := strings.Split(blobPath, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}
