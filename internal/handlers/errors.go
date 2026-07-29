package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
	"xprem/internal/services"
)

type APIError struct {
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

// RenderError enforces the structured RFC 7807 error format
func RenderError(w http.ResponseWriter, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(APIError{
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}

// RenderThrottled is the single response every rate-limited endpoint gives, and
// it says nothing beyond "too many, wait".
//
// The wording is fixed on purpose. It must not vary with which limit was
// reached, nor with whether the account named exists, because a caller able to
// tell those apart can enumerate accounts by watching how the refusal changes.
// One message for every case is what makes the counters unobservable from
// outside.
func RenderThrottled(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	RenderError(w, http.StatusTooManyRequests, "Too many attempts. Try again later.")
}

// RenderJSON writes payload as a JSON body with the given status. The success
// counterpart to RenderError, shared so every handler renders the same way.
func RenderJSON(w http.ResponseWriter, status int, payload interface{}) {
	marshaledResponse, err := json.Marshal(payload)
	if err != nil {
		RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(marshaledResponse)
}

// activeRolloutConflictMessage is the CLI-facing 409 body for publish, republish and
// rollback attempts against a branch and runtime version with an active per-update
// rollout. The republish and rollback commands print it verbatim.
const activeRolloutConflictMessage = "A progressive rollout is already active for this branch and runtime version. Finish or revert it from the dashboard first."

// RenderCliAuthError distinguishes a credential that failed to authenticate
// (401, generic message so nothing leaks about why) from one that
// authenticated but is blocked by per-key access restrictions (403, with the
// reason so the CLI user knows what to fix).
func RenderCliAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, services.ErrCliAccessDenied) {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	http.Error(w, "Error validating auth", http.StatusUnauthorized)
}
