package bucket

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAzureMigrationHistoryConditions(t *testing.T) {
	for _, operation := range []string{"apply", "remove", "create"} {
		t.Run(operation, func(t *testing.T) {
			var mu sync.Mutex
			history, version, writes := "existing\n", 1, 0
			if operation == "remove" {
				history += "target\n"
			} else if operation == "create" {
				history, version = "", 0
			}
			if !azureTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path != "/test/prefix/.migrationhistory" {
					t.Errorf("unexpected object path: %s", r.URL.Path)
				}
				if r.Method == http.MethodGet {
					if version == 0 {
						w.Header().Set("x-ms-error-code", "BlobNotFound")
						w.WriteHeader(http.StatusNotFound)
						return
					}
					w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
					fmt.Fprint(w, history)
					return
				}
				writes++
				if version == 0 {
					if r.Header.Get("If-None-Match") != "*" {
						t.Errorf("creation must require an absent blob")
					}
				} else if r.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, version) {
					t.Errorf("expected If-Match=%d, got %q", version, r.Header.Get("If-Match"))
				}
				if writes == 1 && operation != "create" {
					history += "concurrent\n"
					version++
					w.Header().Set("x-ms-error-code", "ConditionNotMet")
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				content, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read upload: %v", err)
				}
				history = string(content)
				w.Header().Set("ETag", `"3"`)
				w.WriteHeader(http.StatusCreated)
			}) {
				return
			}
			b := &AzureBucket{ContainerName: "test", KeyPrefix: "prefix/"}
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
