package keyStore

import (
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"xprem/config"
	"xprem/internal/crypto"
	"xprem/internal/providers/aws"
)

// GetPublicExpoKey returns the PEM-encoded Expo public signing key for the
// given app, resolving it from the app's KeysConfig (local file, AWS Secrets
// Manager, or inline b64). Returns "" on any read failure, callers that need
// to differentiate "not configured" from "read error" should use
// ReadExpoKey with the "public" selector.
func GetPublicExpoKey(app config.AppConfig) string {
	return readExpoKey(app, true)
}

// GetPrivateExpoKey mirrors GetPublicExpoKey for the private half.
func GetPrivateExpoKey(app config.AppConfig) string {
	return readExpoKey(app, false)
}

// AppKeyAAD binds a mode=database sealed key blob to the app id it belongs to
// and to which half of the pair it is, as AES-GCM additional authenticated data.
//
// Every app seals under the same deployment-wide master key, so without this
// binding any sealed blob decrypts cleanly from any row: a staging dump restored
// over prod, a botched migration, or a future query joining the wrong row would
// hand back a valid-looking key of the wrong app, and the mistake would only
// surface as a signature rejection on every already-installed client. Binding
// makes the unseal fail at the point of the mistake instead.
//
// The value is derived, never stored, it is rebuilt at unseal time from the
// app id of the row the blob was actually read from, which is what makes the
// check meaningful. Changing this format invalidates every existing blob.
func AppKeyAAD(appId string, public bool) []byte {
	half := "private"
	if public {
		half = "public"
	}
	return []byte(appId + "|" + half)
}

// ReadDBKeysMasterKey retrieves the 32-byte symmetric master key as a raw
// binary string. To avoid the logistical overhead and lack of native OpenSSL standard
// commands for symmetric PEM files, the secret is strictly managed as a standard 44-character Base64 text string.
// It decodes the payload at runtime into its 32 binary characters, supporting both production
// (AWS Secrets Manager) and local development (flat environment variables) workflows.
func ReadDBKeysMasterKey() string {
	if secretId := config.GetEnv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID"); secretId != "" {
		b64Secret := aws.FetchSecret(secretId)
		if b64Secret == "" {
			return ""
		}
		return decodeB64(b64Secret)
	}
	if b64Env := config.GetEnv("DB_KEYS_MASTER_KEY_B64"); b64Env != "" {
		return decodeB64(b64Env)
	}
	return ""
}

func readExpoKey(app config.AppConfig, public bool) string {
	k := app.Keys
	switch k.Mode {
	case config.KeysModeLocal:
		if public {
			return readPEMFile(k.PublicPath)
		}
		return readPEMFile(k.PrivatePath)
	case config.KeysModeAWSSM:
		if public {
			return aws.FetchSecret(k.PublicSecretId)
		}
		return aws.FetchSecret(k.PrivateSecretId)
	case config.KeysModeEnvironment:
		if public {
			return decodeB64(k.PublicB64)
		}
		return decodeB64(k.PrivateB64)
	case config.KeysModeDatabase:
		aad := AppKeyAAD(app.Id, public)
		if public {
			key, err := crypto.UnsealAESGCM(k.SealedPublicKey, []byte(ReadDBKeysMasterKey()), aad)
			if err != nil {
				log.Printf("Failed to unseal sealed public key for app %q: %v", app.Id, err)
				return ""
			}
			return string(key)
		}
		key, err := crypto.UnsealAESGCM(k.SealedPrivateKey, []byte(ReadDBKeysMasterKey()), aad)
		if err != nil {
			log.Printf("Failed to unseal sealed private key for app %q: %v", app.Id, err)
			return ""
		}
		return string(key)
	}
	return ""
}

// GetPrivateCloudfrontKey is deployment-global (one CDN per server) so it
// stays on plain env vars and is not part of the per-app config.
//
// When KEYS_STORAGE_TYPE is set it selects the CloudFront key source, the same
// way it selects the Expo key source in stateless mode:
// aws-secrets-manager → AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID,
// environment → PRIVATE_CLOUDFRONT_KEY_B64, local → PRIVATE_CLOUDFRONT_KEY_PATH.
// A source set for a different mode is deliberately ignored (with a one-time
// warning) instead of silently winning over the configured mode.
//
// When KEYS_STORAGE_TYPE is unset the sources are tried in order: AWSSM,
// then B64, then PATH.
func GetPrivateCloudfrontKey() string {
	secretId := config.GetEnv("AWSSM_CLOUDFRONT_PRIVATE_KEY_SECRET_ID")
	b64 := config.GetEnv("PRIVATE_CLOUDFRONT_KEY_B64")
	path := config.GetEnv("PRIVATE_CLOUDFRONT_KEY_PATH")
	mode := config.GetEnv("KEYS_STORAGE_TYPE")
	switch mode {
	case "aws-secrets-manager":
		warnCloudfrontSourceMismatch(mode, secretId == "", b64 != "" || path != "")
		if secretId == "" {
			return ""
		}
		return aws.FetchSecret(secretId)
	case "environment":
		warnCloudfrontSourceMismatch(mode, b64 == "", secretId != "" || path != "")
		return decodeB64(b64)
	case "local":
		warnCloudfrontSourceMismatch(mode, path == "", secretId != "" || b64 != "")
		return readPEMFile(path)
	}
	if secretId != "" {
		return aws.FetchSecret(secretId)
	}
	if b64 != "" {
		return decodeB64(b64)
	}
	if path != "" {
		return readPEMFile(path)
	}
	return ""
}

var cloudfrontSourceMismatchOnce sync.Once

// warnCloudfrontSourceMismatch logs once when KEYS_STORAGE_TYPE selects an
// empty CloudFront key source while another source is set, the exact
// misconfiguration that used to be silently masked by the old
// first-source-wins behavior, and that now disables the CDN.
func warnCloudfrontSourceMismatch(mode string, selectedEmpty, otherSet bool) {
	if !selectedEmpty || !otherSet {
		return
	}
	cloudfrontSourceMismatchOnce.Do(func() {
		log.Printf("KEYS_STORAGE_TYPE=%s selects an empty CloudFront private key source while another CloudFront key variable is set; the other variable is ignored and the CloudFront CDN stays disabled", mode)
	})
}

func readPEMFile(path string) string {
	if path == "" {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		fmt.Println("Error opening key file:", err)
		return ""
	}
	defer file.Close()
	content, err := io.ReadAll(file)
	if err != nil {
		fmt.Println("Error reading key file:", err)
		return ""
	}
	return string(content)
}

func decodeB64(b64 string) string {
	if b64 == "" {
		return ""
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		log.Printf("Failed to decode base64 key: %v", err)
		return ""
	}
	return string(decoded)
}
