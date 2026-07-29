// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package apikeyrestrictions

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"xprem/internal/services"
)

type fakeRestrictionRepo struct {
	restrictions      map[int64]ApiKeyRestrictions
	protectedBranches map[string]bool

	setApiKeyID          int64
	setCanAccess         bool
	setAllowedIps        []netip.Prefix
	setCalls             int
	setBranchCalls       int
	setBranchName        string
	setBranchProtectedTo bool
}

func (f *fakeRestrictionRepo) GetRestrictionsByAppID(ctx context.Context, appID string) ([]ApiKeyRestrictions, error) {
	var out []ApiKeyRestrictions
	for _, restriction := range f.restrictions {
		out = append(out, restriction)
	}
	return out, nil
}

func (f *fakeRestrictionRepo) SetRestrictions(ctx context.Context, appID string, apiKeyID int64, canAccessProtectedBranches bool, allowedIps []netip.Prefix) error {
	f.setCalls++
	f.setApiKeyID = apiKeyID
	f.setCanAccess = canAccessProtectedBranches
	f.setAllowedIps = allowedIps
	return nil
}

func (f *fakeRestrictionRepo) GetRestrictions(ctx context.Context, apiKeyID int64) (ApiKeyRestrictions, error) {
	return f.restrictions[apiKeyID], nil
}

func (f *fakeRestrictionRepo) SetBranchProtection(ctx context.Context, appID string, branchName string, protected bool) error {
	f.setBranchCalls++
	f.setBranchName = branchName
	f.setBranchProtectedTo = protected
	return nil
}

func (f *fakeRestrictionRepo) IsBranchProtected(ctx context.Context, appID string, branchName string) (bool, error) {
	return f.protectedBranches[branchName], nil
}

func (f *fakeRestrictionRepo) GetApiKeyName(ctx context.Context, appID string, apiKeyID int64) (string, error) {
	if apiKeyID == 42 {
		return "ci-production", nil
	}
	return "", fmt.Errorf("unknown key")
}

func serviceWith(repo ApiKeyRestrictionRepository, licensed bool) *ApiKeyRestrictionService {
	service := NewApiKeyRestrictionService(repo)
	service.licenseValid = func() bool { return licensed }
	return service
}

func mustAddr(t *testing.T, value string) netip.Addr {
	t.Helper()
	addr, err := netip.ParseAddr(value)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestStatelessModeAnswersControlPlaneError(t *testing.T) {
	service := serviceWith(nil, true)
	if _, err := service.GetRestrictionsByApp(context.Background(), "app"); !errors.Is(err, ErrRequiresControlPlane) {
		t.Fatalf("expected ErrRequiresControlPlane, got %v", err)
	}
	if err := service.SetRestrictions(context.Background(), "app", 1, false, nil); !errors.Is(err, ErrRequiresControlPlane) {
		t.Fatalf("expected ErrRequiresControlPlane, got %v", err)
	}
	if err := service.SetBranchProtection(context.Background(), "app", "main", true); !errors.Is(err, ErrRequiresControlPlane) {
		t.Fatalf("expected ErrRequiresControlPlane, got %v", err)
	}
	// Enforcement is a no-op in stateless mode, never an error.
	if err := service.AuthorizeCliRequest(context.Background(), "app", 1, "main", netip.Addr{}); err != nil {
		t.Fatalf("expected enforcement no-op, got %v", err)
	}
}

func TestMutationsRequireValidLicense(t *testing.T) {
	repo := &fakeRestrictionRepo{}
	service := serviceWith(repo, false)
	if err := service.SetRestrictions(context.Background(), "app", 1, true, nil); !errors.Is(err, ErrRequiresValidLicense) {
		t.Fatalf("expected ErrRequiresValidLicense, got %v", err)
	}
	if err := service.SetBranchProtection(context.Background(), "app", "main", true); !errors.Is(err, ErrRequiresValidLicense) {
		t.Fatalf("expected ErrRequiresValidLicense, got %v", err)
	}
	if repo.setCalls != 0 || repo.setBranchCalls != 0 {
		t.Fatal("repository must not be touched without a valid license")
	}
}

// Reads stay open without a license so the dashboard can always show which
// restrictions exist on a key.
func TestGetRestrictionsDoesNotRequireLicense(t *testing.T) {
	repo := &fakeRestrictionRepo{restrictions: map[int64]ApiKeyRestrictions{7: {ApiKeyID: 7}}}
	service := serviceWith(repo, false)
	restrictions, err := service.GetRestrictionsByApp(context.Background(), "app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(restrictions) != 1 || restrictions[0].ApiKeyID != 7 {
		t.Fatalf("unexpected restrictions: %+v", restrictions)
	}
}

// SetRestrictions runs entries through parseCidrs (its edge cases are
// covered in cidr_test.go) and persists the normalized result together with
// the branch grant.
func TestSetRestrictionsPersistsNormalizedAllowlist(t *testing.T) {
	repo := &fakeRestrictionRepo{}
	service := serviceWith(repo, true)
	err := service.SetRestrictions(context.Background(), "app", 1, true, []string{"192.168.1.5/24", "::ffff:10.1.2.3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repo.setCanAccess {
		t.Fatal("expected canAccessProtectedBranches to be persisted")
	}
	expected := []netip.Prefix{
		netip.MustParsePrefix("192.168.1.0/24"),
		netip.MustParsePrefix("10.1.2.3/32"),
	}
	if !reflect.DeepEqual(repo.setAllowedIps, expected) {
		t.Fatalf("unexpected allowed ips: %v", repo.setAllowedIps)
	}
}

func TestSetRestrictionsRejectsInvalidCidr(t *testing.T) {
	repo := &fakeRestrictionRepo{}
	service := serviceWith(repo, true)
	err := service.SetRestrictions(context.Background(), "app", 1, false, []string{"not-an-ip"})
	if !errors.Is(err, ErrInvalidCidr) {
		t.Fatalf("expected ErrInvalidCidr, got %v", err)
	}
	if repo.setCalls != 0 {
		t.Fatal("repository must not be touched on invalid input")
	}
}

// An empty allowlist must reach the repository as nil so the column stores
// NULL (no restriction) instead of an empty array.
func TestSetRestrictionsEmptyAllowlistIsNil(t *testing.T) {
	repo := &fakeRestrictionRepo{}
	service := serviceWith(repo, true)
	if err := service.SetRestrictions(context.Background(), "app", 1, false, []string{"", "  "}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.setAllowedIps != nil {
		t.Fatalf("expected nil allowlist, got %v", repo.setAllowedIps)
	}
}

func TestAuthorizeEnforcesIpAllowlist(t *testing.T) {
	repo := &fakeRestrictionRepo{
		restrictions: map[int64]ApiKeyRestrictions{1: {
			ApiKeyID:   1,
			AllowedIps: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		}},
	}
	service := serviceWith(repo, true)
	if err := service.AuthorizeCliRequest(context.Background(), "app", 1, "", mustAddr(t, "10.1.2.3")); err != nil {
		t.Fatalf("allowlisted address rejected: %v", err)
	}
	// The rejection names the resolved caller IP so an operator can debug the
	// allowlist, while staying an ErrIpNotAllowed (mapped to a 403).
	err := service.AuthorizeCliRequest(context.Background(), "app", 1, "", mustAddr(t, "203.0.113.9"))
	if !errors.Is(err, ErrIpNotAllowed) {
		t.Fatalf("expected ErrIpNotAllowed, got %v", err)
	}
	if !errors.Is(err, services.ErrCliAccessDenied) {
		t.Fatalf("expected the error to map to a CLI access denial, got %v", err)
	}
	if !strings.Contains(err.Error(), "203.0.113.9") {
		t.Fatalf("expected the rejected IP in the message, got %q", err.Error())
	}
	// An allowlist with an unresolvable caller address never passes, and the
	// message hints at the proxy configuration that usually causes it.
	err = service.AuthorizeCliRequest(context.Background(), "app", 1, "", netip.Addr{})
	if !errors.Is(err, ErrIpNotAllowed) {
		t.Fatalf("expected ErrIpNotAllowed for invalid address, got %v", err)
	}
	if !strings.Contains(err.Error(), "TRUST_PROXY_HEADERS") {
		t.Fatalf("expected a proxy hint for the unresolved IP, got %q", err.Error())
	}
}

// Regression for the parse/enforce mismatch: an allowlist entered in mapped
// form must actually admit the caller it designates, whether that caller
// arrives as plain IPv4 or in mapped form, and still exclude everyone else.
func TestAuthorizeMatchesAllowlistEnteredInMappedForm(t *testing.T) {
	repo := &fakeRestrictionRepo{restrictions: map[int64]ApiKeyRestrictions{}}
	service := serviceWith(repo, true)
	err := service.SetRestrictions(context.Background(), "app", 1, false,
		[]string{"::ffff:203.0.113.7", "::ffff:10.0.0.0/104"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The fake repository does not wire Set to Get; feed the stored prefixes
	// back so AuthorizeCliRequest evaluates exactly what was persisted.
	repo.restrictions[1] = ApiKeyRestrictions{ApiKeyID: 1, AllowedIps: repo.setAllowedIps}

	for _, caller := range []string{"203.0.113.7", "::ffff:203.0.113.7", "10.20.30.40"} {
		if err := service.AuthorizeCliRequest(context.Background(), "app", 1, "", mustAddr(t, caller)); err != nil {
			t.Fatalf("caller %q: allowlisted address rejected: %v", caller, err)
		}
	}
	for _, caller := range []string{"203.0.113.8", "2001:db8::1"} {
		if err := service.AuthorizeCliRequest(context.Background(), "app", 1, "", mustAddr(t, caller)); !errors.Is(err, ErrIpNotAllowed) {
			t.Fatalf("caller %q: expected ErrIpNotAllowed, got %v", caller, err)
		}
	}
}

func TestAuthorizeDeniesProtectedBranchWithoutGrant(t *testing.T) {
	repo := &fakeRestrictionRepo{
		restrictions:      map[int64]ApiKeyRestrictions{1: {ApiKeyID: 1}},
		protectedBranches: map[string]bool{"production": true},
	}
	service := serviceWith(repo, true)
	err := service.AuthorizeCliRequest(context.Background(), "app", 1, "production", netip.Addr{})
	if !errors.Is(err, ErrBranchProtected) {
		t.Fatalf("expected ErrBranchProtected, got %v", err)
	}
}

func TestAuthorizeAllowsProtectedBranchWithGrant(t *testing.T) {
	repo := &fakeRestrictionRepo{
		restrictions:      map[int64]ApiKeyRestrictions{1: {ApiKeyID: 1, CanAccessProtectedBranches: true}},
		protectedBranches: map[string]bool{"production": true},
	}
	service := serviceWith(repo, true)
	if err := service.AuthorizeCliRequest(context.Background(), "app", 1, "production", netip.Addr{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthorizeAllowsUnprotectedBranchForAnyKey(t *testing.T) {
	repo := &fakeRestrictionRepo{
		restrictions:      map[int64]ApiKeyRestrictions{1: {ApiKeyID: 1}},
		protectedBranches: map[string]bool{"production": true},
	}
	service := serviceWith(repo, true)
	// staging is not protected, and a brand-new branch is not protected
	// either: both stay open to a default key.
	for _, branch := range []string{"staging", "brand-new-branch"} {
		if err := service.AuthorizeCliRequest(context.Background(), "app", 1, branch, netip.Addr{}); err != nil {
			t.Fatalf("branch %q: unexpected error: %v", branch, err)
		}
	}
}

func TestAuthorizeSkipsBranchCheckForBranchlessRequests(t *testing.T) {
	repo := &fakeRestrictionRepo{
		restrictions:      map[int64]ApiKeyRestrictions{1: {ApiKeyID: 1}},
		protectedBranches: map[string]bool{"production": true},
	}
	service := serviceWith(repo, true)
	if err := service.AuthorizeCliRequest(context.Background(), "app", 1, "", netip.Addr{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthorizeIsNoOpWithoutValidLicense(t *testing.T) {
	repo := &fakeRestrictionRepo{
		restrictions: map[int64]ApiKeyRestrictions{1: {
			ApiKeyID:   1,
			AllowedIps: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		}},
		protectedBranches: map[string]bool{"production": true},
	}
	service := serviceWith(repo, false)
	if err := service.AuthorizeCliRequest(context.Background(), "app", 1, "production", mustAddr(t, "203.0.113.9")); err != nil {
		t.Fatalf("expected community behavior without license, got %v", err)
	}
}
