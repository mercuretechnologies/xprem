package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"xprem/internal/android/androidtest"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

type buildCredentialRepository struct {
	services.CredentialsRepository
	credentials *repository.SealedAndroidCredentials
	err         error
	id          string
}

func (repo *buildCredentialRepository) UpsertAndroidCredentials(_ context.Context, id string, credentials repository.SealedAndroidCredentials) error {
	repo.credentials = &credentials
	return nil
}
func (repo *buildCredentialRepository) GetAndroidCredentials(_ context.Context, id string) (*repository.SealedAndroidCredentials, error) {
	repo.id = id
	return repo.credentials, repo.err
}

type buildIdentifierRepository struct {
	services.AppIdentifierRepository
	app, id   string
	allocated int64
}

func (repo *buildIdentifierRepository) GetAppIdentifierByID(_ context.Context, app, id string) (*repository.AppIdentifierRef, error) {
	repo.app, repo.id = app, id
	if app != "app-1" || id != "id-1" {
		return nil, nil
	}
	return &repository.AppIdentifierRef{Id: id, Platform: types.PlatformAndroid}, nil
}
func (repo *buildIdentifierRepository) AllocateBuildNumber(_ context.Context, app, id string, next func(types.Platform, string) (string, error)) (*repository.AppIdentifierRef, error) {
	repo.app, repo.id = app, id
	previous := strconv.FormatInt(repo.allocated, 10)
	value, err := next(types.PlatformAndroid, previous)
	if err != nil {
		return nil, err
	}
	repo.allocated++
	return &repository.AppIdentifierRef{Id: "id-1", Platform: types.PlatformAndroid, BuildNumber: value, PreviousBuildNumber: previous}, nil
}
func TestBuildCredentialsAllowlistAndWholeFile(t *testing.T) {
	t.Setenv("AWSSM_DB_KEYS_MASTER_KEY_SECRET_ID", "")
	t.Setenv("DB_KEYS_MASTER_KEY_B64", base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")))
	raw := androidtest.JKSKeystore("store-password", "key-password", "selected-alias")
	repo := &buildCredentialRepository{}
	identifiers := &buildIdentifierRepository{}
	credentials := services.NewCredentialsService(repo, identifiers)
	require.NoError(t, credentials.SaveAndroidCredentials(context.Background(), "app-1", "id-1", services.AndroidCredentialsInput{
		KeystoreBase64: base64.StdEncoding.EncodeToString(raw), KeystorePassword: "store-password", KeyAlias: "selected-alias", KeyPassword: "key-password",
	}))
	unreadablePlaySecret := "not a decryptable Google Play secret"
	repo.credentials.SealedGoogleServiceAccountKey = &unreadablePlaySecret
	h := NewBuildHandler(nil, credentials, nil, nil)
	req := mux.SetURLVars(buildTestRequest("GET"), map[string]string{"APP_ID": "app-1", "IDENTIFIER_ID": "untrusted-route-id"})
	w := httptest.NewRecorder()
	h.AndroidCredentials(w, req)
	require.Equal(t, 200, w.Code)
	var got map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Equal(t, map[string]string{"keystore": base64.StdEncoding.EncodeToString(raw), "keystorePassword": "store-password", "keyAlias": "selected-alias", "keyPassword": "key-password"}, got)
	require.Equal(t, "app-1", identifiers.app)
	require.Equal(t, "id-1", repo.id)
}
func TestBuildCredentialsErrorsDoNotExposeSecrets(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
	}{
		{errors.New("secret sentinel"), 500},
		{&repository.ErrResourceNotFound{Resource: "android credentials", Identifier: "id"}, 404},
		{repository.ErrNotSupportedInStatelessMode, 400},
	} {
		h := NewBuildHandler(nil, services.NewCredentialsService(&buildCredentialRepository{err: tc.err}, &buildIdentifierRepository{}), nil, nil)
		w := httptest.NewRecorder()
		h.AndroidCredentials(w, mux.SetURLVars(buildTestRequest("GET"), map[string]string{"APP_ID": "app-1", "IDENTIFIER_ID": "untrusted-route-id"}))
		require.Equal(t, tc.status, w.Code)
		require.NotContains(t, w.Body.String(), "secret sentinel")
	}
}
func TestBuildEnvironmentRejectsAmbiguousQuery(t *testing.T) {
	for _, query := range []string{"channel=a&environment=b", "channel=a&channel=b", "environment=", "channel=", "other=x", "environment=%zz", "environment=a;b"} {
		t.Run(query, func(t *testing.T) {
			h := NewBuildHandler(nil, nil, nil, nil) // invalid requests cannot reach the service
			w := httptest.NewRecorder()
			h.Environment(w, httptest.NewRequest("GET", "/?"+query, nil))
			require.Equal(t, 400, w.Code, w.Body.String())
		})
	}
}
func TestAllocateBuildNumber(t *testing.T) {
	for _, tc := range []struct {
		allocated int64
		status    int
		body      string
	}{
		{0, 200, `{"buildNumber":"1"}`},
		{2_100_000_000, 409, `{"title":"Conflict","detail":"build number limit reached","status":409}`},
	} {
		repo := &buildIdentifierRepository{allocated: tc.allocated}
		h := NewBuildHandler(nil, nil, nil, services.NewAppIdentifierService(repo))
		w := httptest.NewRecorder()
		h.AllocateBuildNumber(w, mux.SetURLVars(buildTestRequest("POST"), map[string]string{"APP_ID": "app-1", "IDENTIFIER_ID": "untrusted-route-id"}))
		require.Equal(t, tc.status, w.Code, w.Body.String())
		require.JSONEq(t, tc.body, w.Body.String())
		require.Equal(t, "app-1", repo.app)
		require.Equal(t, "id-1", repo.id)
	}
}

func buildTestRequest(method string) *http.Request {
	req := httptest.NewRequest(method, "/", nil)
	return req.WithContext(services.WithBuildIdentifier(req.Context(), "id-1"))
}

func TestBuildHandlersRequireResolvedIdentifier(t *testing.T) {
	h := NewBuildHandler(nil, nil, nil, nil)
	for name, handler := range map[string]http.HandlerFunc{
		"resolve":     h.ResolveIdentifier,
		"credentials": h.AndroidCredentials,
		"counter":     h.AllocateBuildNumber,
	} {
		t.Run(name, func(t *testing.T) {
			req := mux.SetURLVars(httptest.NewRequest("GET", "/", nil), map[string]string{"APP_ID": "app-1", "IDENTIFIER_ID": "id-1"})
			recorder := httptest.NewRecorder()
			handler(recorder, req)
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
		})
	}
}

func TestBuildResolveUsesContext(t *testing.T) {
	h := NewBuildHandler(nil, nil, nil, nil)
	req := mux.SetURLVars(buildTestRequest("GET"), map[string]string{"IDENTIFIER_ID": "untrusted-route-id"})
	recorder := httptest.NewRecorder()
	h.ResolveIdentifier(recorder, req)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.JSONEq(t, `{"identifierId":"id-1"}`, recorder.Body.String())
}
