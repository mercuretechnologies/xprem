package helpers

import (
	"net/http"
	"path/filepath"
	"strings"
	"xprem/internal/types"
)

func GetAuth(r *http.Request) types.Auth {
	bearerToken, _ := GetBearerToken(r)
	if bearerToken != "" {
		return types.Auth{
			Token: &bearerToken,
		}
	}
	sessionSecret := r.Header.Get("expo-session")
	if sessionSecret != "" {
		return types.Auth{
			SessionSecret: &sessionSecret,
		}
	}
	return types.Auth{}
}

func GetBearerToken(r *http.Request) (string, error) {
	bearerToken := r.Header.Get("Authorization")
	if bearerToken == "" {
		return "", nil
	}
	tokens := strings.Split(bearerToken, "Bearer ")
	if len(tokens) != 2 {
		return "", nil
	}
	return tokens[1], nil
}

func MaskSecret(value string) string {
	if len(value) < 5 {
		return "***"
	}
	return "***" + value[:5]
}

func MaskKeyPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	fileName := filepath.Base(trimmed)
	if fileName == "." || fileName == string(filepath.Separator) {
		return ".../[Configured File]"
	}
	return ".../" + fileName
}
