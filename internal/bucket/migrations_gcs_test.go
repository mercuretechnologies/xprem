package bucket

import (
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGCSMigrationHistoryConditions(t *testing.T) {
	for _, operation := range []string{"apply", "remove", "create"} {
		t.Run(operation, func(t *testing.T) {
			// The provider caches its client; isolate emulator configuration from
			// credentials and client setup used by other tests in the package.
			const childEnv = "XPREM_GCS_MIGRATION_TEST_CHILD"
			if os.Getenv(childEnv) != t.Name() {
				cmd := exec.Command(os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$")
				cmd.Env = append(os.Environ(), childEnv+"="+t.Name())
				output, err := cmd.CombinedOutput()
				require.NoError(t, err, "%s", output)
				return
			}
			var mu sync.Mutex
			history, generation, writes := "existing\n", 1, 0
			if operation == "remove" {
				history += "target\n"
			} else if operation == "create" {
				history, generation = "", 0
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.Method == http.MethodGet {
					if generation == 0 {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("X-Goog-Generation", strconv.Itoa(generation))
					fmt.Fprint(w, history)
					return
				}
				writes++
				expectedGeneration := r.URL.Query().Get("ifGenerationMatch")
				if expectedGeneration != strconv.Itoa(generation) {
					t.Errorf("expected ifGenerationMatch=%d, got %q", generation, expectedGeneration)
				}
				if writes == 1 && operation != "create" {
					history += "concurrent\n"
					generation++
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusPreconditionFailed)
					fmt.Fprint(w, `{"error":{"code":412,"message":"generation changed"}}`)
					return
				}
				_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					t.Errorf("parse upload content type: %v", err)
					return
				}
				parts := multipart.NewReader(r.Body, params["boundary"])
				for {
					part, err := parts.NextPart()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Errorf("read upload part: %v", err)
						return
					}
					content, err := io.ReadAll(part)
					if err != nil {
						t.Errorf("read upload content: %v", err)
					}
					history = string(content) // The object body follows the metadata part.
				}
				generation++
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"generation":"%d","size":"%d"}`, generation, len(history))
			}))
			defer server.Close()
			t.Setenv("STORAGE_EMULATOR_HOST", server.URL)
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS_B64", "")
			b := &GCSBucket{BucketName: "test", KeyPrefix: "prefix/"}
			var err error
			if operation == "remove" {
				err = b.RemoveMigrationFromHistory("target")
			} else {
				err = b.ApplyMigration("target")
			}
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			switch operation {
			case "apply":
				require.Equal(t, "existing\nconcurrent\ntarget\n", history)
			case "remove":
				require.Equal(t, "existing\nconcurrent\n", history)
			case "create":
				require.Equal(t, "target\n", history)
			}
		})
	}
}
