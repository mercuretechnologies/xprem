package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// stallingBody sends a first byte and then never sends the rest, the way a
// slowloris client does.
type stallingBody struct {
	sentFirst bool
	release   chan struct{}
}

func (b *stallingBody) Read(p []byte) (int, error) {
	if !b.sentFirst {
		b.sentFirst = true
		p[0] = '{'
		return 1, nil
	}
	<-b.release
	return 0, io.EOF
}

func (b *stallingBody) Close() error { return nil }

// TestReadDeadlineRefusesABodyThatNeverArrives runs against a real server,
// since the deadline lives on the connection and a ResponseRecorder has none.
func TestReadDeadlineRefusesABodyThatNeverArrives(t *testing.T) {
	router := mux.NewRouter()
	router.Use(NewReadDeadlineMiddleware(150 * time.Millisecond))
	readErr := make(chan error, 1)
	router.HandleFunc("/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		readErr <- err
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodPost)

	server := httptest.NewServer(router)
	defer server.Close()

	body := &stallingBody{release: make(chan struct{})}
	defer close(body.release)
	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/logs", body)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	// -1 forces chunked encoding, so the server keeps reading instead of trusting a length.
	req.ContentLength = -1

	start := time.Now()
	resp, err := server.Client().Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	elapsed := time.Since(start)

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("the handler read a body that was never sent: no deadline applied")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing gave up: the request is still being held")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("the deadline did not bite, the request ran for %s", elapsed)
	}
}

// TestReadDeadlineLetsANormalBodyThrough checks a body sent in one go is not
// affected by the deadline.
func TestReadDeadlineLetsANormalBodyThrough(t *testing.T) {
	router := mux.NewRouter()
	router.Use(NewReadDeadlineMiddleware(5 * time.Second))
	router.HandleFunc("/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading a body that was sent whole: %v", err)
		}
		if string(payload) != `{"resourceLogs":[]}` {
			t.Errorf("the handler must see the body unchanged, got %q", payload)
		}
		w.WriteHeader(http.StatusOK)
	}).Methods(http.MethodPost)

	server := httptest.NewServer(router)
	defer server.Close()

	resp, err := server.Client().Post(
		server.URL+"/v1/logs", "application/json", stringReader(`{"resourceLogs":[]}`),
	)
	if err != nil {
		t.Fatalf("an honest request must succeed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// TestReadDeadlineIsSilentWhereItCannotApply checks that a ResponseWriter
// that cannot carry a deadline, like httptest.ResponseRecorder, does not
// turn into a 500.
func TestReadDeadlineIsSilentWhereItCannotApply(t *testing.T) {
	router := mux.NewRouter()
	router.Use(NewReadDeadlineMiddleware(time.Second))
	router.HandleFunc("/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}).Methods(http.MethodPost)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/logs", nil))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("the handler must still run, got %d", recorder.Code)
	}
}

type stringReaderT struct{ s string }

func (r *stringReaderT) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}

func stringReader(s string) io.Reader { return &stringReaderT{s: s} }
