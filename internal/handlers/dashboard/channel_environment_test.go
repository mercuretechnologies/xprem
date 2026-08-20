package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"xprem/internal/services"
	"xprem/internal/store"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// channelEnvRepo records every channel binding written; environments resolve
// to "<name>-id".
type channelEnvRepo struct {
	written []*string
}

func (r *channelEnvRepo) InsertEnvironment(_ context.Context, _, _ string) (string, error) {
	return "", nil
}
func (r *channelEnvRepo) ListEnvironments(_ context.Context, _ string) ([]store.EnvironmentRow, error) {
	return nil, nil
}
func (r *channelEnvRepo) GetEnvironmentIdByName(_ context.Context, _, name string) (string, error) {
	return name + "-id", nil
}
func (r *channelEnvRepo) DeleteEnvironment(_ context.Context, _, _ string) error { return nil }
func (r *channelEnvRepo) UpsertEnvVar(_ context.Context, _, _ string, _ bool, _ string) error {
	return nil
}
func (r *channelEnvRepo) ListEnvVars(_ context.Context, _ string) ([]store.EnvVarRow, error) {
	return nil, nil
}
func (r *channelEnvRepo) GetSealedValue(_ context.Context, _, _ string) (*string, error) {
	return nil, nil
}
func (r *channelEnvRepo) DeleteEnvVar(_ context.Context, _, _ string) error { return nil }
func (r *channelEnvRepo) SetChannelEnvironment(_ context.Context, _, _ string, environmentId *string) error {
	r.written = append(r.written, environmentId)
	return nil
}

func setChannelEnvironment(t *testing.T, body string) (*httptest.ResponseRecorder, *channelEnvRepo) {
	t.Helper()
	repo := &channelEnvRepo{}
	handler := NewEnvironmentsHandler(services.NewEnvironmentService(repo))

	r := httptest.NewRequest(http.MethodPut, "/channels/prod/environment", strings.NewReader(body))
	r = mux.SetURLVars(r, map[string]string{"APP_ID": "app-1", "CHANNEL": "prod"})
	w := httptest.NewRecorder()
	handler.SetChannelEnvironmentHandler(w, r)
	return w, repo
}

func TestSetChannelEnvironmentBinds(t *testing.T) {
	w, repo := setChannelEnvironment(t, `{"environment":"staging"}`)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Len(t, repo.written, 1)
	require.NotNil(t, repo.written[0])
	assert.Equal(t, "staging-id", *repo.written[0])
}

func TestSetChannelEnvironmentExplicitNullUnbinds(t *testing.T) {
	w, repo := setChannelEnvironment(t, `{"environment":null}`)

	require.Equal(t, http.StatusNoContent, w.Code)
	require.Len(t, repo.written, 1)
	assert.Nil(t, repo.written[0])
}

// A body without the key is refused: reading it as null would unbind the
// channel on a typo.
func TestSetChannelEnvironmentRefusesMissingKey(t *testing.T) {
	for _, body := range []string{`{}`, `{"environmentName":"staging"}`, `not json`, `{"environment":42}`} {
		w, repo := setChannelEnvironment(t, body)

		assert.Equal(t, http.StatusBadRequest, w.Code, body)
		assert.Empty(t, repo.written, body)
	}
}
