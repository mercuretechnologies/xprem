package bucket

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Azure caches its client globally, so endpoint tests need isolated provider
// configuration without affecting the optional Azurite integration tests.
func azureTestServer(t *testing.T, handler http.HandlerFunc) bool {
	t.Helper()
	const childEnv = "XPREM_AZURE_TEST_CHILD"
	if os.Getenv(childEnv) != t.Name() {
		cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.timeout=15s")
		cmd.Env = append(os.Environ(), childEnv+"="+t.Name())
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", output)
		return false
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	t.Setenv("AZURE_BLOB_ENDPOINT", server.URL)
	t.Setenv("AZURE_STORAGE_ACCOUNT_NAME", "testaccount")
	t.Setenv("AZURE_STORAGE_ACCOUNT_KEY", "dGVzdC1henVyZS1zaGFyZWQta2V5")
	return true
}

func TestAzurePagerErrorCancelsWorkers(t *testing.T) {
	for _, operation := range []string{"delete", "copy"} {
		t.Run(operation, func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			stop := make(chan struct{})
			if !azureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("comp") == "list" {
					w.Header().Set("Content-Type", "application/xml")
					if r.URL.Query().Get("marker") == "" {
						fmt.Fprint(w, `<EnumerationResults><Blobs><Blob><Name>app-1/main/1.0/123/bundle.js</Name><Properties><BlobType>BlockBlob</BlobType></Properties></Blob></Blobs><NextMarker>next</NextMarker></EnumerationResults>`)
						return
					}
					select {
					case <-started:
					case <-stop:
						return
					}
					w.Header().Set("x-ms-error-code", "AuthorizationFailure")
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `<Error><Code>AuthorizationFailure</Code><Message>second page denied</Message></Error>`)
					return
				}
				close(started)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-stop:
				}
			}) {
				return
			}
			t.Cleanup(func() { close(stop) })
			b := &AzureBucket{ContainerName: "test"}
			var err error
			if operation == "delete" {
				err = b.deletePrefix(context.Background(), "app-1/main/1.0/123/")
			} else {
				update := validUpdate()
				_, err = b.CreateUpdateFrom(&update, "456")
			}
			require.ErrorContains(t, err, "failed to list blobs")
			select {
			case <-canceled:
			case <-time.After(2 * time.Second):
				t.Fatal("worker request remained active after the pager failed")
			}
		})
	}
}
