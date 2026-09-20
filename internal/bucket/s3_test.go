package bucket

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"xprem/internal/types"

	"github.com/stretchr/testify/require"
)

// The provider caches its S3 client globally. Run endpoint tests in a child
// process so other tests can still initialize their own provider configuration.
func s3TestServer(t *testing.T, handler http.HandlerFunc) bool {
	t.Helper()
	const childEnv = "XPREM_S3_TEST_CHILD"
	if os.Getenv(childEnv) != t.Name() {
		cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
		cmd.Env = append(os.Environ(), childEnv+"="+t.Name())
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return false
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("AWS_REGION", "us-east-1")
	t.Setenv("AWS_ACCESS_KEY_ID", "s3-test-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "s3-test-secret")
	t.Setenv("AWS_BASE_ENDPOINT", server.URL)
	t.Setenv("AWS_S3_FORCE_PATH_STYLE", "true")
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	return true
}

func s3ListResponse(w http.ResponseWriter, prefixes []string, nextToken string) {
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprintf(w, "<ListBucketResult><IsTruncated>%t</IsTruncated>", nextToken != "")
	if nextToken != "" {
		fmt.Fprintf(w, "<NextContinuationToken>%s</NextContinuationToken>", nextToken)
	}
	for _, prefix := range prefixes {
		fmt.Fprintf(w, "<CommonPrefixes><Prefix>%s</Prefix></CommonPrefixes>", prefix)
	}
	fmt.Fprint(w, "</ListBucketResult>")
}

func TestS3DeletePrefixRejectsPartialFailure(t *testing.T) {
	if !s3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		if r.Method == http.MethodGet {
			fmt.Fprint(w, "<ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>prefix/asset</Key></Contents></ListBucketResult>")
			return
		}
		fmt.Fprint(w, "<DeleteResult><Error><Key>prefix/asset</Key><Code>AccessDenied</Code><Message>deletion denied</Message></Error></DeleteResult>")
	}) {
		return
	}
	err := (&S3Bucket{BucketName: "test"}).deletePrefix(context.Background(), "prefix/")
	require.ErrorContains(t, err, "prefix/asset")
	require.ErrorContains(t, err, "deletion denied")
}

func TestS3UpdateListingPagination(t *testing.T) {
	if !s3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		query := r.URL.Query()
		prefix := query.Get("prefix")
		secondPage := query.Get("continuation-token") != ""
		if strings.Contains(prefix, "error") && secondPage && (!strings.Contains(prefix, "outer-error") || strings.Count(prefix, "/") == 3) {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>second page denied</Message></Error>")
			return
		}
		if query.Get("delimiter") != "/" {
			t.Errorf("missing delimiter in %s", r.URL)
		}
		var suffix string
		switch strings.Count(prefix, "/") {
		case 2: // key prefix + app: branches
			suffix = "main/"
			if secondPage {
				suffix = "staging/"
			}
		case 3: // runtime versions
			suffix = "1.0/"
			if secondPage {
				suffix = "2.0/"
			}
		case 4: // updates within each runtime
			suffix = "1700000000000/"
			if secondPage {
				suffix = "1700000060000/"
			}
		default:
			t.Errorf("unexpected prefix %q", prefix)
		}
		next := "next-page"
		if secondPage {
			next = ""
		}
		s3ListResponse(w, []string{prefix + suffix}, next)
	}) {
		return
	}
	b := &S3Bucket{BucketName: "test", KeyPrefix: "prefix/"}
	branches, err := b.GetBranches("app")
	require.NoError(t, err)
	require.Equal(t, []string{"main", "staging"}, branches)
	updates, err := b.GetUpdates("app", "main", "1.0")
	require.NoError(t, err)
	require.Len(t, updates, 2)
	require.Equal(t, "1700000060000", updates[1].UpdateId)
	runtimes, err := b.GetRuntimeVersions("app", "main")
	require.NoError(t, err)
	require.Len(t, runtimes, 2)
	for _, version := range runtimes {
		require.Equal(t, 2, version.NumberOfUpdates)
		require.Equal(t, "2023-11-14T22:13:20Z", version.CreatedAt)
		require.Equal(t, "2023-11-14T22:14:20Z", version.LastUpdatedAt)
	}
	_, err = b.GetBranches("error")
	require.ErrorContains(t, err, "ListObjectsV2 error")
	_, err = b.GetUpdates("error", "main", "1.0")
	require.ErrorContains(t, err, "ListObjectsV2 error")
	_, err = b.GetRuntimeVersions("error", "main")
	require.ErrorContains(t, err, "ListObjectsV2 error in updates")
	_, err = b.GetRuntimeVersions("outer-error", "main")
	require.ErrorContains(t, err, "ListObjectsV2 error:")
}

func TestS3GetFileWithoutLastModified(t *testing.T) {
	if !s3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "asset body")
	}) {
		return
	}
	b := &S3Bucket{BucketName: "test"}
	for _, fetch := range []func() (*types.BucketFile, error){
		func() (*types.BucketFile, error) { return b.GetFile(types.Update{}, "asset") },
		func() (*types.BucketFile, error) { return b.getObject(context.Background(), "asset") },
	} {
		file, err := fetch()
		require.NoError(t, err)
		require.True(t, file.CreatedAt.IsZero())
		body, err := io.ReadAll(file.Reader)
		require.NoError(t, err)
		require.Equal(t, "asset body", string(body))
		require.NoError(t, file.Reader.Close())
	}
}

func TestS3MoveRootEntriesUnderProtectsDestination(t *testing.T) {
	for _, tc := range []struct {
		name              string
		destination       string
		interrupt         bool
		verificationFails bool
		wantError         string
	}{
		{name: "new copy"},
		{name: "matching interrupted copy", destination: "asset content"},
		{name: "conflicting destination", destination: "different content", wantError: "differs"},
		{name: "verification error", destination: "asset content", verificationFails: true, wantError: "AccessDenied"},
		{name: "resume after delete failure", interrupt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const assetKey = "prefix/main/1.0/1700000000/asset.js"
			const markerKey = "prefix/main/1.0/1700000000/.check"
			const destinationKey = "prefix/app/main/1.0/1700000000/asset.js"
			objects := map[string]string{assetKey: "asset content", markerKey: "check"}
			if tc.destination != "" {
				objects[destinationKey] = tc.destination
			}
			var mu sync.Mutex
			interrupted := false
			if !s3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/xml")
				key := strings.TrimPrefix(r.URL.Path, "/test/")
				if r.URL.Query().Get("list-type") == "2" {
					fmt.Fprint(w, "<ListBucketResult><IsTruncated>false</IsTruncated>")
					keys := make([]string, 0, len(objects))
					for objectKey := range objects {
						keys = append(keys, objectKey)
					}
					sort.Strings(keys)
					for _, objectKey := range keys {
						if strings.HasPrefix(objectKey, r.URL.Query().Get("prefix")) {
							fmt.Fprintf(w, "<Contents><Key>%s</Key></Contents>", objectKey)
						}
					}
					fmt.Fprint(w, "</ListBucketResult>")
					return
				}
				switch r.Method {
				case http.MethodPut:
					if r.Header.Get("If-None-Match") != "*" {
						t.Error("copy must require an absent destination")
					}
					if _, exists := objects[key]; exists {
						w.WriteHeader(http.StatusPreconditionFailed)
						fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
						return
					}
					sourceKey := strings.TrimPrefix(r.Header.Get("X-Amz-Copy-Source"), "test/")
					objects[key] = objects[sourceKey]
					fmt.Fprint(w, "<CopyObjectResult><ETag>\"copied-etag\"</ETag></CopyObjectResult>")
				case http.MethodGet:
					if tc.verificationFails && key == destinationKey {
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
						return
					}
					// Different ETags can still represent identical copied bytes.
					w.Header().Set("ETag", fmt.Sprintf("%q", key))
					fmt.Fprint(w, objects[key])
				case http.MethodDelete:
					if tc.interrupt && key == assetKey && !interrupted {
						interrupted = true
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
						return
					}
					delete(objects, key)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
			}) {
				return
			}
			b := &S3Bucket{BucketName: "test", KeyPrefix: "prefix/"}
			err := b.MoveRootEntriesUnder("app")
			if tc.interrupt {
				require.ErrorContains(t, err, "delete")
				err = b.MoveRootEntriesUnder("app")
			}
			mu.Lock()
			defer mu.Unlock()
			if tc.wantError != "" {
				require.ErrorContains(t, err, tc.wantError)
				require.Equal(t, "asset content", objects[assetKey])
				require.Equal(t, "check", objects[markerKey])
				require.Equal(t, tc.destination, objects[destinationKey])
				return
			}
			require.NoError(t, err)
			require.NotContains(t, objects, assetKey)
			require.NotContains(t, objects, markerKey)
			require.Equal(t, "asset content", objects[destinationKey])
		})
	}
}
