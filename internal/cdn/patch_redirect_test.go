package cdn

import (
	"errors"
	testing2 "testing"
)

func TestSupportsPatchRedirectOnlyWithAnEdge(t *testing2.T) {
	t.Run("no CDN", func(t *testing2.T) {
		clearCDNEnv(t)
		ResetCDNInstance()
		t.Cleanup(ResetCDNInstance)
		if SupportsPatchRedirect() {
			t.Fatalf("expected no patch redirect without a CDN")
		}
	})
	t.Run("s3 direct", func(t *testing2.T) {
		clearCDNEnv(t)
		setS3CDNEnv(t)
		ResetCDNInstance()
		t.Cleanup(ResetCDNInstance)
		if SupportsPatchRedirect() {
			t.Fatalf("expected no patch redirect on s3-direct, got %q", ResolvedType())
		}
	})
	t.Run("generic", func(t *testing2.T) {
		clearCDNEnv(t)
		setS3CDNEnv(t)
		t.Setenv("CDN_BASE_URL", "https://cdn.example.com")
		ResetCDNInstance()
		t.Cleanup(ResetCDNInstance)
		if !SupportsPatchRedirect() {
			t.Fatalf("expected patch redirect on generic, got %q", ResolvedType())
		}
	})
}

func TestGenericComputeRedirectionURLForPatch(t *testing2.T) {
	clearCDNEnv(t)
	setS3CDNEnv(t)
	t.Setenv("CDN_BASE_URL", "https://cdn.example.com/")
	t.Setenv("BUCKET_KEY_PREFIX", "prefix/")
	ResetCDNInstance()
	t.Cleanup(ResetCDNInstance)

	url, err := GetCDN().ComputeRedirectionURLForPatch("app", "main", "target-uuid", "source-uuid")
	if err != nil {
		t.Fatal(err)
	}
	if want := "https://cdn.example.com/prefix/app/bsdiff/main/target-uuid/source-uuid"; url != want {
		t.Fatalf("got %q, want %q", url, want)
	}
}

func TestS3DirectRefusesPatchRedirect(t *testing2.T) {
	clearCDNEnv(t)
	setS3CDNEnv(t)
	ResetCDNInstance()
	t.Cleanup(ResetCDNInstance)

	_, err := GetCDN().ComputeRedirectionURLForPatch("app", "main", "target-uuid", "source-uuid")
	if !errors.Is(err, ErrPatchRedirectUnsupported) {
		t.Fatalf("got %v, want ErrPatchRedirectUnsupported", err)
	}
}
