package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"
	"xprem/config"

	"cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

var (
	gcsClient     *storage.Client
	gcsClientErr  error
	initGCSClient sync.Once
)

func GetClient() (*storage.Client, error) {
	initGCSClient.Do(func() {
		ctx := context.Background()
		var opts []option.ClientOption
		b64Creds := config.GetEnv("GOOGLE_APPLICATION_CREDENTIALS_B64")
		if b64Creds != "" {
			creds, err := base64.StdEncoding.DecodeString(b64Creds)
			if err != nil {
				gcsClientErr = fmt.Errorf("error decoding GOOGLE_APPLICATION_CREDENTIALS_B64: %w", err)
				return
			}
			opts = append(opts, option.WithCredentialsJSON(creds))
		}
		gcsClient, gcsClientErr = storage.NewClient(ctx, opts...)
		if gcsClientErr != nil {
			gcsClientErr = fmt.Errorf("error initializing GCS client: %w", gcsClientErr)
		}
	})
	return gcsClient, gcsClientErr
}

type googleCreds struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

func loadGoogleCreds() (*googleCreds, error) {
	b64Creds := config.GetEnv("GOOGLE_APPLICATION_CREDENTIALS_B64")
	if b64Creds == "" {
		return nil, errors.New("GOOGLE_APPLICATION_CREDENTIALS_B64 not set")
	}
	creds, err := base64.StdEncoding.DecodeString(b64Creds)
	if err != nil {
		return nil, fmt.Errorf("failed to decode GOOGLE_APPLICATION_CREDENTIALS_B64: %w", err)
	}
	var c googleCreds
	if err := json.Unmarshal(creds, &c); err != nil {
		return nil, fmt.Errorf("parse GOOGLE_APPLICATION_CREDENTIALS: %w", err)
	}
	if c.ClientEmail == "" || c.PrivateKey == "" {
		return nil, errors.New("credentials missing client_email or private_key")
	}
	return &c, nil
}

func SignedURL(bucket, key, method, contentType string, expires time.Duration, headers ...string) (string, error) {
	return signedURL(bucket, key, &storage.SignedURLOptions{
		Method:      method,
		Expires:     time.Now().Add(expires),
		ContentType: contentType,
		Headers:     headers,
	})
}

func SignedDownloadURL(bucket, key, contentDisposition, contentType string, expiresAt time.Time) (string, error) {
	return signedURL(bucket, key, &storage.SignedURLOptions{
		Method:  "GET",
		Expires: expiresAt,
		QueryParameters: url.Values{
			"response-content-disposition": {contentDisposition},
			"response-content-type":        {contentType},
		},
	})
}

func signedURL(bucket, key string, opts *storage.SignedURLOptions) (string, error) {
	creds, err := loadGoogleCreds()
	if err != nil {
		return "", err
	}
	opts.Scheme = storage.SigningSchemeV4
	opts.GoogleAccessID = creds.ClientEmail
	opts.PrivateKey = []byte(creds.PrivateKey)
	return storage.SignedURL(bucket, key, opts)
}
