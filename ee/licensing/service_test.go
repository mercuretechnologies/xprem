// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package licensing

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeLicenseRepo is an in-memory LicenseRepository mirroring the singleton
// row of the enterprise_license table.
type fakeLicenseRepo struct {
	stored    *StoredLicense
	secret    string
	secretErr error
	err       error
	saveHook  func(ctx context.Context)
}

func (r *fakeLicenseRepo) GetLicense(ctx context.Context) (*StoredLicense, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.stored == nil {
		return nil, nil
	}
	snapshot := *r.stored
	return &snapshot, nil
}

func (r *fakeLicenseRepo) GetActivationSecret(ctx context.Context) (string, error) {
	if r.err != nil {
		return "", r.err
	}
	if r.secretErr != nil {
		return "", r.secretErr
	}
	return r.secret, nil
}

func (r *fakeLicenseRepo) SaveActivation(ctx context.Context, key string, activationSecret string, license License) (StoredLicense, error) {
	if r.saveHook != nil {
		r.saveHook(ctx)
	}
	if r.err != nil {
		return StoredLicense{}, r.err
	}
	now := time.Now().UTC()
	r.secret = activationSecret
	r.stored = &StoredLicense{
		Key:             key,
		License:         license,
		ActivatedAt:     now,
		LastValidatedAt: &now,
	}
	return *r.stored, nil
}

func (r *fakeLicenseRepo) MarkValidated(ctx context.Context, license License) (StoredLicense, error) {
	if r.err != nil {
		return StoredLicense{}, r.err
	}
	now := time.Now().UTC()
	r.stored.License = license
	r.stored.LastValidatedAt = &now
	r.stored.ValidationFailedAt = nil
	r.stored.ValidationErrorCode = ""
	return *r.stored, nil
}

func (r *fakeLicenseRepo) MarkValidationFailed(ctx context.Context, errorCode string) (StoredLicense, error) {
	if r.err != nil {
		return StoredLicense{}, r.err
	}
	if r.stored.ValidationFailedAt == nil {
		now := time.Now().UTC()
		r.stored.ValidationFailedAt = &now
	}
	r.stored.ValidationErrorCode = errorCode
	return *r.stored, nil
}

func (r *fakeLicenseRepo) DeleteLicense(ctx context.Context) error {
	if r.err != nil {
		return r.err
	}
	r.stored = nil
	return nil
}

func newTestService(t *testing.T, repo LicenseRepository, client *Client) *LicenseService {
	t.Helper()
	Deactivate()
	t.Cleanup(Deactivate)
	return NewLicenseService(repo, client, "instance-1", "https://updates.example.com")
}

const proInformationsJSON = `{
	"org": {"name": "Acme Corp"},
	"planCode": "pro",
	"subscription": {"startAt": "2026-01-01T00:00:00.000Z", "endAt": null, "renewalAt": null}
}`

func checkAnswersValidAcme(w http.ResponseWriter, r *http.Request) {
	writeJSONBody(w, http.StatusOK, `{"valid": true, "licenseInformations": `+acmeInformationsJSON+`}`)
}

func repoWithAcme() *fakeLicenseRepo {
	now := time.Now().UTC()
	return &fakeLicenseRepo{
		secret: "secret-42",
		stored: &StoredLicense{
			Key:             "XPREM-KEY",
			License:         acmeLicense(),
			ActivatedAt:     now.Add(-24 * time.Hour),
			LastValidatedAt: &now,
		},
	}
}

func TestServiceStatelessModeAnswersControlPlaneError(t *testing.T) {
	service := newTestService(t, nil, NewClient(""))
	if _, err := service.Status(context.Background()); !errors.Is(err, ErrLicenseRequiresControlPlane) {
		t.Fatalf("expected ErrLicenseRequiresControlPlane, got %v", err)
	}
	if _, err := service.Check(context.Background(), "XPREM-KEY"); !errors.Is(err, ErrLicenseRequiresControlPlane) {
		t.Fatalf("expected ErrLicenseRequiresControlPlane, got %v", err)
	}
	if _, err := service.Attach(context.Background(), "XPREM-KEY"); !errors.Is(err, ErrLicenseRequiresControlPlane) {
		t.Fatalf("expected ErrLicenseRequiresControlPlane, got %v", err)
	}
	if err := service.Remove(context.Background()); !errors.Is(err, ErrLicenseRequiresControlPlane) {
		t.Fatalf("expected ErrLicenseRequiresControlPlane, got %v", err)
	}
	if err := service.ActivateFromStore(context.Background()); err != nil {
		t.Fatalf("expected boot activation to be a no-op in stateless mode, got %v", err)
	}
	// Must not panic nor call the network.
	service.ValidateNow(context.Background())
}

func TestAttachPersistsActivationAndEnablesEnterprise(t *testing.T) {
	repo := &fakeLicenseRepo{}
	fake := &fakeLicenseServer{
		check: checkAnswersValidAcme,
		attach: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"isActive": true, "activationSecret": "secret-42", "licenseInformations": `+acmeInformationsJSON+`}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))

	status, err := service.Attach(context.Background(), "XPREM-KEY")
	require.NoError(t, err)
	assert.True(t, status.Valid())
	require.NotNil(t, repo.stored)
	assert.Equal(t, "XPREM-KEY", repo.stored.Key)
	assert.Equal(t, "secret-42", repo.secret)
	assert.Equal(t, "Acme Corp", repo.stored.License.OrgName)
	assert.True(t, IsEnterprise())
}

func TestAttachRefusalLeavesStoreAndEditionUntouched(t *testing.T) {
	repo := &fakeLicenseRepo{}
	fake := &fakeLicenseServer{
		check: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"valid": false, "errorCode": "LICENSE_KEY_ALREADY_USED"}`)
		},
		attach: func(w http.ResponseWriter, r *http.Request) {
			t.Error("attach must not be called for a key the check refused")
		},
	}
	service := newTestService(t, repo, fake.client(t))

	_, err := service.Attach(context.Background(), "XPREM-KEY")
	var refusal *DecisionError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, CodeLicenseKeyAlreadyUsed, refusal.Code)
	assert.Nil(t, repo.stored)
	assert.False(t, IsEnterprise())
}

func TestCheckHasNoSideEffect(t *testing.T) {
	repo := &fakeLicenseRepo{}
	fake := &fakeLicenseServer{
		check: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"valid": true, "licenseInformations": `+acmeInformationsJSON+`}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))

	result, err := service.Check(context.Background(), "XPREM-KEY")
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Nil(t, repo.stored)
	assert.False(t, IsEnterprise(), "a check must not activate anything")
}

func TestCheckRefusesUnsupportedPlan(t *testing.T) {
	repo := &fakeLicenseRepo{}
	fake := &fakeLicenseServer{
		check: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"valid": true, "licenseInformations": `+proInformationsJSON+`}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))

	result, err := service.Check(context.Background(), "XPREM-KEY")
	require.NoError(t, err)
	assert.False(t, result.Valid)
	assert.Equal(t, CodePlanNotSupported, result.ErrorCode)
}

func TestAttachRefusesUnsupportedPlanBeforeConsumingKey(t *testing.T) {
	repo := &fakeLicenseRepo{}
	fake := &fakeLicenseServer{
		check: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"valid": true, "licenseInformations": `+proInformationsJSON+`}`)
		},
		attach: func(w http.ResponseWriter, r *http.Request) {
			t.Error("attach must not consume a key whose plan is unsupported")
		},
	}
	service := newTestService(t, repo, fake.client(t))

	_, err := service.Attach(context.Background(), "XPREM-KEY")
	var refusal *DecisionError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, CodePlanNotSupported, refusal.Code)
	assert.Nil(t, repo.stored)
	assert.False(t, IsEnterprise())
}

func TestValidateNowPlanDowngradeStartsGrace(t *testing.T) {
	repo := repoWithAcme()
	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"isActive": true, "licenseInformations": `+proInformationsJSON+`}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	require.NotNil(t, repo.stored.ValidationFailedAt)
	assert.Equal(t, CodePlanNotSupported, repo.stored.ValidationErrorCode)
	assert.True(t, IsEnterprise(), "the grace window applies to a plan downgrade too")
}

func TestValidateNowSuccessClearsFailureAndRefreshesLicense(t *testing.T) {
	repo := repoWithAcme()
	failedAt := time.Now().Add(-time.Hour).UTC()
	repo.stored.ValidationFailedAt = &failedAt
	repo.stored.ValidationErrorCode = CodeServerUnreachable

	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			refreshed := `{
				"org": {"name": "Acme Corp"},
				"planCode": "enterprise",
				"subscription": {"startAt": "2026-01-01T00:00:00.000Z", "endAt": null, "renewalAt": "2028-01-01T00:00:00.000Z"}
			}`
			writeJSONBody(w, http.StatusOK, `{"isActive": true, "licenseInformations": `+refreshed+`}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))

	service.ValidateNow(context.Background())

	assert.Nil(t, repo.stored.ValidationFailedAt)
	assert.Empty(t, repo.stored.ValidationErrorCode)
	require.NotNil(t, repo.stored.License.SubscriptionRenewalAt)
	assert.Equal(t, 2028, repo.stored.License.SubscriptionRenewalAt.Year(), "a successful validation refreshes the descriptor")
	assert.True(t, IsEnterprise())
}

func TestValidateNowRefusalStartsGraceAndKeepsEnterpriseOn(t *testing.T) {
	repo := repoWithAcme()
	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusBadRequest, `{"valid": false, "errorCode": "SUBSCRIPTION_INACTIVE"}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	require.NotNil(t, repo.stored.ValidationFailedAt)
	assert.Equal(t, CodeSubscriptionInactive, repo.stored.ValidationErrorCode)
	assert.True(t, IsEnterprise(), "the grace window keeps enterprise features on")
}

func TestValidateNowKeepsTheFirstFailureAsGraceAnchor(t *testing.T) {
	repo := repoWithAcme()
	anchor := time.Now().Add(-48 * time.Hour).UTC()
	repo.stored.ValidationFailedAt = &anchor
	repo.stored.ValidationErrorCode = CodeServerUnreachable

	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusBadRequest, `{"valid": false, "errorCode": "SUBSCRIPTION_INACTIVE"}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	assert.True(t, repo.stored.ValidationFailedAt.Equal(anchor), "repeat failures must not push the grace deadline back")
	assert.Equal(t, CodeSubscriptionInactive, repo.stored.ValidationErrorCode)
	assert.True(t, IsEnterprise())
}

func TestValidateNowSuspendsOnceGraceIsExhausted(t *testing.T) {
	repo := repoWithAcme()
	anchor := time.Now().Add(-GracePeriod - time.Hour).UTC()
	repo.stored.ValidationFailedAt = &anchor
	repo.stored.ValidationErrorCode = CodeSubscriptionInactive

	fake := &fakeLicenseServer{
		validate: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusBadRequest, `{"valid": false, "errorCode": "SUBSCRIPTION_INACTIVE"}`)
		},
	}
	service := newTestService(t, repo, fake.client(t))
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	assert.False(t, IsEnterprise(), "an exhausted grace window drops the license")
	require.NotNil(t, repo.stored, "the row stays so the dashboard can explain the suspension")
}

func TestValidateNowUnreachableServerCountsAsFailure(t *testing.T) {
	repo := repoWithAcme()
	fake := &fakeLicenseServer{} // no validate handler: 404 answers
	service := newTestService(t, repo, fake.client(t))
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	require.NotNil(t, repo.stored.ValidationFailedAt)
	assert.Equal(t, CodeServerUnreachable, repo.stored.ValidationErrorCode)
	assert.True(t, IsEnterprise())
}

func TestValidateNowSkipsWithoutInstanceIdInsteadOfBurningGrace(t *testing.T) {
	repo := repoWithAcme()
	Deactivate()
	t.Cleanup(Deactivate)
	service := NewLicenseService(repo, NewClient("http://127.0.0.1:1"), "", "https://updates.example.com")
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	assert.Nil(t, repo.stored.ValidationFailedAt, "a missing instance id is a local problem, not a license decision")
	assert.True(t, IsEnterprise())
}

func TestValidateNowSkipsWhenSecretUnreadableInsteadOfBurningGrace(t *testing.T) {
	repo := repoWithAcme()
	repo.secretErr = errors.New("failed to unseal the license activation secret")
	service := newTestService(t, repo, NewClient("http://127.0.0.1:1"))
	Activate(repo.stored.License)

	service.ValidateNow(context.Background())

	assert.Nil(t, repo.stored.ValidationFailedAt, "an unreadable secret is a local problem, not a license decision")
	assert.True(t, IsEnterprise())
}

func TestValidateNowIgnoresShutdownCancellation(t *testing.T) {
	repo := repoWithAcme()
	service := newTestService(t, repo, NewClient("http://127.0.0.1:1"))
	Activate(repo.stored.License)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	service.ValidateNow(cancelled)

	assert.Nil(t, repo.stored.ValidationFailedAt, "a shutdown must not be recorded as a license failure")
	assert.True(t, IsEnterprise())
}

func TestActivateFromStoreWithinGrace(t *testing.T) {
	repo := repoWithAcme()
	failedAt := time.Now().Add(-time.Hour).UTC()
	repo.stored.ValidationFailedAt = &failedAt
	service := newTestService(t, repo, NewClient(""))

	require.NoError(t, service.ActivateFromStore(context.Background()))
	assert.True(t, IsEnterprise())
}

func TestActivateFromStoreSuspendedRunsCommunity(t *testing.T) {
	repo := repoWithAcme()
	failedAt := time.Now().Add(-GracePeriod - time.Hour).UTC()
	repo.stored.ValidationFailedAt = &failedAt
	service := newTestService(t, repo, NewClient(""))

	require.NoError(t, service.ActivateFromStore(context.Background()))
	assert.False(t, IsEnterprise())

	status, err := service.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, status.HasKey)
	assert.True(t, status.Suspended())
	assert.False(t, status.Valid())
}

func TestRemoveDropsToCommunity(t *testing.T) {
	repo := repoWithAcme()
	service := newTestService(t, repo, NewClient(""))
	Activate(repo.stored.License)

	require.NoError(t, service.Remove(context.Background()))
	assert.Nil(t, repo.stored)
	assert.False(t, IsEnterprise())
}

func TestAttachPersistsWhenTheRequestContextIsCancelled(t *testing.T) {
	fake := &fakeLicenseServer{
		check: checkAnswersValidAcme,
		attach: func(w http.ResponseWriter, r *http.Request) {
			writeJSONBody(w, http.StatusOK, `{"isActive": true, "activationSecret": "secret-42", "licenseInformations": `+acmeInformationsJSON+`}`)
		},
	}
	repo := &fakeLicenseRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	repo.saveHook = func(saveCtx context.Context) {
		cancel()
		assert.NoError(t, saveCtx.Err(), "the attach consumed the single-use key; persistence must survive the request context")
	}
	service := newTestService(t, repo, fake.client(t))

	_, err := service.Attach(ctx, "XPREM-KEY")
	require.NoError(t, err)
	require.NotNil(t, repo.stored)
	assert.Equal(t, "secret-42", repo.secret)
}
