package infrastructure

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"xprem/ee/licensing"
	"xprem/ee/rbac"
	dashhandlers "xprem/internal/handlers/dashboard"
	"xprem/internal/repository"
	"xprem/internal/services"
	"xprem/internal/types"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

type counterContractRepo struct {
	services.AppIdentifierRepository
	platform types.Platform
	saved    string
	writes   int
}

func (r *counterContractRepo) GetAppIdentifierByID(_ context.Context, app, id string) (*repository.AppIdentifierRef, error) {
	if app != "app-1" || id != buildID {
		return nil, nil
	}
	return &repository.AppIdentifierRef{Id: id, Platform: r.platform, BuildNumber: "42"}, nil
}
func (r *counterContractRepo) SetBuildNumber(_ context.Context, _, _ string, value string) error {
	r.saved = value
	r.writes++
	return nil
}

type counterContractGrant struct {
	rbac.RBACRepository
	permissions []rbac.Permission
}

func (r counterContractGrant) GetUserAppGrant(context.Context, string, string) (*rbac.AppGrant, error) {
	return &rbac.AppGrant{AppID: "app-1", ExtraPermissions: r.permissions}, nil
}

func TestDashboardCounterTextContractAndPermission(t *testing.T) {
	previous := licensing.Current()
	licensing.Activate(licensing.License{PlanCode: licensing.PlanEnterprise})
	t.Cleanup(func() {
		if previous == nil {
			licensing.Deactivate()
		} else {
			licensing.Activate(*previous)
		}
	})
	for _, tc := range []struct {
		name        string
		platform    types.Platform
		body, saved string
		permission  rbac.Permission
		status      int
	}{
		{"legacy integer", "android", `{"buildNumber":42}`, "42", rbac.PermCredentialsManage, 204},
		{"string zero", "android", `{"buildNumber":"0"}`, "0", rbac.PermCredentialsManage, 204},
		{"android ceiling", "android", `{"buildNumber":"2100000000"}`, "2100000000", rbac.PermCredentialsManage, 204},
		{"android overflow", "android", `{"buildNumber":"2100000001"}`, "", rbac.PermCredentialsManage, 400},
		{"android points", "android", `{"buildNumber":"1.2.0"}`, "", rbac.PermCredentialsManage, 400},
		{"ios points", "ios", `{"buildNumber":"1.2.0"}`, "1.2.0", rbac.PermCredentialsManage, 204},
		{"ios large string", "ios", `{"buildNumber":"9223372036854775808"}`, "9223372036854775808", rbac.PermCredentialsManage, 204},
		{"ios large legacy integer", "ios", `{"buildNumber":9223372036854775808}`, "9223372036854775808", rbac.PermCredentialsManage, 204},
		{"fraction is not dotted", "ios", `{"buildNumber":1.2}`, "", rbac.PermCredentialsManage, 400},
		{"malformed", "ios", `{"buildNumber":"1..2"}`, "", rbac.PermCredentialsManage, 400},
		{"exponent", "ios", `{"buildNumber":1e3}`, "", rbac.PermCredentialsManage, 400},
		{"negative", "ios", `{"buildNumber":"-1"}`, "", rbac.PermCredentialsManage, 400},
		{"null", "ios", `{"buildNumber":null}`, "", rbac.PermCredentialsManage, 400},
		{"missing", "ios", `{}`, "", rbac.PermCredentialsManage, 400},
		{"viewer", "ios", `{"buildNumber":"1.2.0"}`, "", "", 403},
		{"other permission", "android", `{"buildNumber":"1"}`, "", rbac.PermAppRename, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &counterContractRepo{platform: tc.platform}
			access := rbac.NewRBACService(counterContractGrant{permissions: []rbac.Permission{tc.permission}}, nil)
			container := &AppContainer{AppRepo: buildAppRepo{}, RBACService: access, AppIdentifiersHandler: dashhandlers.NewAppIdentifiersHandler(services.NewAppIdentifierService(repo))}
			router := mux.NewRouter()
			registerAppRoutes(router, container)
			request := httptest.NewRequest(http.MethodPut, "/apps/app-1/identifiers/"+buildID+"/build-number", strings.NewReader(tc.body))
			request = request.WithContext(services.WithPrincipal(request.Context(), &services.DashboardPrincipal{UserId: "member-1"}))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, tc.status, response.Code, response.Body.String())
			if tc.status == 204 {
				require.Equal(t, 1, repo.writes)
				require.Equal(t, tc.saved, repo.saved)
			} else {
				require.Zero(t, repo.writes)
			}
		})
	}
}
