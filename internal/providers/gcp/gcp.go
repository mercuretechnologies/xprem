package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

func SignedURL(bucket, key, method, contentType string, expires time.Duration) (string, error) {
	creds, err := loadGoogleCreds()
	if err != nil {
		return "", err
	}
	opts := &storage.SignedURLOptions{
		Scheme:         storage.SigningSchemeV4,
		Method:         method,
		Expires:        time.Now().Add(expires),
		GoogleAccessID: creds.ClientEmail,
		PrivateKey:     []byte(creds.PrivateKey),
	}
	if contentType != "" {
		opts.ContentType = contentType
	}
	return storage.SignedURL(bucket, key, opts)
}
