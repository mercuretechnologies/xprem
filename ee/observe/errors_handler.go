// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"context"
	"errors"
	"log"
	"net/http"
	"xprem/ee/symbolication"
	"xprem/internal/handlers"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type ErrorReader interface {
	ReadErrorGroup(ctx context.Context, appID, updateID, fingerprint string) (*ErrorGroup, error)
}

// IndexStateReader says whether an update's index is ready, with the
// symbolication errors as answers.
type IndexStateReader interface {
	UpdateIndexState(ctx context.Context, appID, updateUUID string) error
}

// ErrorGroupStatus is why an error has no group yet, or "ready".
type ErrorGroupStatus string

const (
	ErrorGroupReady ErrorGroupStatus = "ready"
	// ErrorGroupWaiting: the index is there, the sweep has not passed yet.
	ErrorGroupWaiting     ErrorGroupStatus = "waiting"
	ErrorGroupIndexing    ErrorGroupStatus = "indexing"
	ErrorGroupIndexFailed ErrorGroupStatus = "index_failed"
	ErrorGroupNoSourcemap ErrorGroupStatus = "no_sourcemap"
	// ErrorGroupUnavailable: no license, no source map uploads or no control plane.
	ErrorGroupUnavailable ErrorGroupStatus = "unavailable"
)

type ErrorGroupAnswer struct {
	Status ErrorGroupStatus `json:"status"`
	Group  *ErrorGroup      `json:"group,omitempty"`
}

// ErrorsHandler serves the group of one error.
type ErrorsHandler struct {
	// reader is nil without the control plane.
	reader  ErrorReader
	indexes IndexStateReader
}

// NewErrorsHandler binds error-group and index-state readers. A nil reader,
// including a typed nil *Explorer, makes group requests report unavailable.
func NewErrorsHandler(reader ErrorReader, indexes IndexStateReader) *ErrorsHandler {
	// A nil *Explorer stored in an interface is itself non-nil.
	if explorer, ok := reader.(*Explorer); ok && explorer == nil {
		reader = nil
	}
	return &ErrorsHandler{reader: reader, indexes: indexes}
}

// GetErrorGroupHandler answers GET /observe/errors/{FINGERPRINT}?updateId=:
// the group of one error, or why it has none yet. Invalid UUIDs yield 400;
// reader failures yield 500. An unavailable reader yields a 200 status answer.
func (h *ErrorsHandler) GetErrorGroupHandler(w http.ResponseWriter, r *http.Request) {
	updateID, err := uuid.Parse(r.URL.Query().Get("updateId"))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "updateId must be a UUID.")
		return
	}
	fingerprint, err := uuid.Parse(mux.Vars(r)["FINGERPRINT"])
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "The fingerprint must be a UUID.")
		return
	}
	if h.reader == nil {
		handlers.RenderJSON(w, http.StatusOK, ErrorGroupAnswer{Status: ErrorGroupUnavailable})
		return
	}
	readContext, cancelRead := boundedRead(r)
	defer cancelRead()
	appID := mux.Vars(r)["APP_ID"]
	group, err := h.reader.ReadErrorGroup(readContext, appID, updateID.String(), fingerprint.String())
	if err != nil {
		log.Printf("observe: reading an error group failed: %v", err)
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		return
	}
	if group != nil {
		handlers.RenderJSON(w, http.StatusOK, ErrorGroupAnswer{Status: ErrorGroupReady, Group: group})
		return
	}
	status, err := h.missingGroupStatus(readContext, appID, updateID.String())
	if err != nil {
		log.Printf("observe: reading the index state of update %s failed: %v", updateID, err)
		handlers.RenderError(w, http.StatusInternalServerError, "An internal error occurred.")
		return
	}
	handlers.RenderJSON(w, http.StatusOK, ErrorGroupAnswer{Status: status})
}

// missingGroupStatus tells from the update's index why the sweep has not
// grouped the error, or that it simply has not passed yet. Known index-state
// errors become statuses; unexpected reader errors propagate.
func (h *ErrorsHandler) missingGroupStatus(ctx context.Context, appID, updateID string) (ErrorGroupStatus, error) {
	if h.indexes == nil {
		return ErrorGroupUnavailable, nil
	}
	err := h.indexes.UpdateIndexState(ctx, appID, updateID)
	switch {
	case err == nil:
		return ErrorGroupWaiting, nil
	case errors.Is(err, symbolication.ErrIndexNotReady):
		return ErrorGroupIndexing, nil
	case errors.Is(err, symbolication.ErrIndexFailed):
		return ErrorGroupIndexFailed, nil
	case errors.Is(err, symbolication.ErrNoSourcemap), errors.Is(err, symbolication.ErrUpdateNotFound):
		return ErrorGroupNoSourcemap, nil
	case errors.Is(err, symbolication.ErrUnavailable):
		return ErrorGroupUnavailable, nil
	}
	return "", err
}
