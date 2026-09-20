package bucket

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestS3MigrationHistoryPreservesConcurrentChanges(t *testing.T) {
	for _, remove := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "remove"}[remove], func(t *testing.T) {
			var mu sync.Mutex
			history := "existing\n"
			if remove {
				history += "target\n"
			}
			version, reads, writes := 1, 0, 0
			if !s3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if r.URL.Path != "/test/prefix/.migrationhistory" {
					t.Errorf("unexpected object path: %s", r.URL.Path)
				}
				if r.Method == http.MethodGet {
					reads++
					w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
					fmt.Fprint(w, history)
					return
				}
				writes++
				if writes == 1 {
					history += "concurrent\n"
					version++
				}
				if r.Header.Get("If-Match") != fmt.Sprintf(`"%d"`, version) {
					w.WriteHeader(http.StatusPreconditionFailed)
					fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
					return
				}
				content, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read write body: %v", err)
				}
				history = string(content)
				w.Header().Set("ETag", `"3"`)
			}) {
				return
			}
			b := &S3Bucket{BucketName: "test", KeyPrefix: "prefix/"}
			var err error
			if remove {
				err = b.RemoveMigrationFromHistory("target")
			} else {
				err = b.ApplyMigration("target")
			}
			require.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			require.Equal(t, 2, reads)
			require.Equal(t, 2, writes)
			if remove {
				require.Equal(t, "existing\nconcurrent\n", history)
			} else {
				require.Equal(t, "existing\nconcurrent\ntarget\n", history)
			}
		})
	}
}

func TestS3MigrationHistoryCreatesConditionally(t *testing.T) {
	var mu sync.Mutex
	writes := 0
	if !s3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		writes++
		if r.Header.Get("If-None-Match") != "*" {
			t.Errorf("first write must require an absent object; headers: %v", r.Header)
		}
		content, err := io.ReadAll(r.Body)
		if err != nil || string(content) != "target\n" {
			t.Errorf("unexpected history write %q: %v", content, err)
		}
	}) {
		return
	}
	require.NoError(t, (&S3Bucket{BucketName: "test"}).ApplyMigration("target"))
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 1, writes)
}

func TestS3MigrationHistoryReadFailuresNeverWrite(t *testing.T) {
	for _, failure := range []string{"forbidden", "missing bucket", "truncated", "malformed"} {
		t.Run(failure, func(t *testing.T) {
			if !s3TestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("read failure must not write history")
				}
				w.Header().Set("ETag", `"1"`)
				switch failure {
				case "forbidden":
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
				case "missing bucket":
					w.WriteHeader(http.StatusNotFound)
					fmt.Fprint(w, "<Error><Code>NoSuchBucket</Code></Error>")
				case "truncated":
					w.Header().Set("Content-Length", "100")
					fmt.Fprint(w, "first\npartial")
				case "malformed":
					fmt.Fprint(w, "first\nsecond unexpected\nthird\n")
				}
			}) {
				return
			}
			b := &S3Bucket{BucketName: "test"}
			_, err := b.RetrieveMigrationHistory()
			require.Error(t, err)
			require.Error(t, b.ApplyMigration("target"))
			require.Error(t, b.RemoveMigrationFromHistory("first"))
		})
	}
}
