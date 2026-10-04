package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"xprem/internal/bucket"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

type shareRedirectStore struct {
	services.BuildArtifactStore
	deadline       time.Time
	ref            bucket.BuildArtifact
	signed, opened bool
	err            error
}

func (s *shareRedirectStore) PresignGet(_ context.Context, ref bucket.BuildArtifact, deadline time.Time) (string, error) {
	s.ref, s.deadline, s.signed = ref, deadline, true
	return "https://bucket.example.com/build.apk?signature=private", s.err
}

func (s *shareRedirectStore) Get(context.Context, bucket.BuildArtifact, bool) (*types.BucketFile, error) {
	s.opened = true
	return nil, errors.New("must not open the artifact for a redirect")
}

func TestPublicShareRedirectsWithoutOpeningArtifact(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expiresIn time.Duration
		revoked   bool
		err       error
		status    int
	}{
		{"short URL", time.Hour, false, nil, http.StatusFound},
		{"bounded by share expiry", 30 * time.Second, false, nil, http.StatusFound},
		{"expired", -time.Minute, false, nil, http.StatusBadRequest},
		{"revoked", time.Hour, true, nil, http.StatusBadRequest},
		{"signing failure", time.Hour, false, errors.New("private signing details"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			share := types.BuildShare{ExpiresAt: now.Add(tc.expiresIn)}
			if tc.revoked {
				share.RevokedAt = &now
			}
			token := strings.Repeat("a", 64)
			hash := sha256.Sum256([]byte(token))
			repo := newRegistryRepo()
			repo.shares[hex.EncodeToString(hash[:])] = share
			repo.builds[registryBuild] = types.BuildRecord{ID: registryBuild, AppID: registryApp, AppIdentifierID: registryIdentifier, Status: types.BuildStatusReady, ArtifactType: types.BuildArtifactAPK}
			storage := &shareRedirectStore{err: tc.err}
			handler := NewBuildRegistryHandler(services.NewBuildService(repo, nil, storage))
			request := mux.SetURLVars(httptest.NewRequest(http.MethodGet, "/build-shares/"+token, nil), map[string]string{"TOKEN": token})
			response := httptest.NewRecorder()
			handler.PublicShare(response, request)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			require.False(t, storage.opened)
			require.Equal(t, "no-store", response.Header().Get("Cache-Control"))
			require.Equal(t, "no-referrer", response.Header().Get("Referrer-Policy"))
			if tc.status == http.StatusFound {
				require.Equal(t, "https://bucket.example.com/build.apk?signature=private", response.Header().Get("Location"))
				require.Empty(t, response.Body.String(), "redirects do not render HTML")
				require.Equal(t, registryBuild, storage.ref.BuildID)
				require.False(t, storage.deadline.After(share.ExpiresAt))
				require.False(t, storage.deadline.After(time.Now().Add(time.Minute)))
				require.True(t, storage.deadline.After(now))
			} else {
				require.Empty(t, response.Header().Get("Location"))
				require.NotContains(t, response.Body.String(), "private signing details")
				if tc.status == http.StatusBadRequest {
					require.False(t, storage.signed)
					require.Equal(t, "Expired link\n", response.Body.String())
				}
			}
		})
	}
}
