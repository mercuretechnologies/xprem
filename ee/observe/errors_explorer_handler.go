// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package observe

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"xprem/internal/handlers"
)

func (h *ExplorerHandler) requireErrorsLicense(w http.ResponseWriter) bool {
	if h.licenseValid == nil || !h.licenseValid() {
		handlers.RenderError(w, http.StatusForbidden, "Error tracking requires a valid Enterprise license.")
		return false
	}
	return true
}

func parseErrorFatality(raw string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return "all", true
	case "fatal":
		return "fatal", true
	case "nonfatal", "non_fatal":
		return "nonfatal", true
	default:
		return "", false
	}
}

func parseErrorLimit(raw string) (int, error) {
	if raw == "" {
		return 50, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 100 {
		return 0, ErrInvalidErrorsQuery
	}
	return value, nil
}

func (h *ExplorerHandler) renderErrorsQueryError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrInvalidErrorID) {
		handlers.RenderError(w, http.StatusBadRequest, "The errorId is invalid.")
	} else if errors.Is(err, ErrInvalidErrorsQuery) {
		handlers.RenderError(w, http.StatusBadRequest, "The Errors query is invalid.")
	} else {
		h.renderQueryError(w, err)
	}
}

func isErrorsValidationError(err error) bool {
	return errors.Is(err, ErrInvalidErrorID) || errors.Is(err, ErrInvalidErrorsQuery) ||
		errors.Is(err, errInvalidObserveRange) || errors.Is(err, errInvalidObservePlatform) ||
		errors.Is(err, errInvalidObserveFilter) || errors.Is(err, errInvalidIdentityFilter) ||
		errors.Is(err, errObserveCohortTooLarge)
}

func (h *ExplorerHandler) GetErrorsHandler(w http.ResponseWriter, r *http.Request) {
	if !h.requireErrorsLicense(w) {
		return
	}
	base, err := h.parseBaseQuery(r, ErrorsMaxWindow)
	if err != nil {
		h.renderQueryError(w, err)
		return
	}
	if len(base.Conditions) > 0 {
		h.renderErrorsQueryError(w, ErrInvalidErrorsQuery)
		return
	}
	values := r.URL.Query()
	fatality, ok := parseErrorFatality(values.Get("fatality"))
	if !ok {
		handlers.RenderError(w, http.StatusBadRequest, "'fatality' must be all, fatal or non_fatal.")
		return
	}
	search := strings.TrimSpace(values.Get("search"))
	if len(search) > 256 {
		handlers.RenderError(w, http.StatusBadRequest, "'search' must be at most 256 characters.")
		return
	}
	sort := values.Get("sort")
	if sort == "" {
		sort = "occurrences"
	}
	switch sort {
	case "occurrences", "impactedDevices", "lastSeen":
	default:
		handlers.RenderError(w, http.StatusBadRequest, "'sort' must be occurrences, impactedDevices or lastSeen.")
		return
	}
	limit, err := parseErrorLimit(values.Get("limit"))
	if err != nil {
		h.renderErrorsQueryError(w, err)
		return
	}
	offset := 0
	if raw := values.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 || offset > 10000 {
			h.renderErrorsQueryError(w, ErrInvalidErrorsQuery)
			return
		}
	}
	includeSeries := false
	if raw := values.Get("includeSeries"); raw != "" {
		includeSeries, err = strconv.ParseBool(raw)
		if err != nil {
			h.renderErrorsQueryError(w, ErrInvalidErrorsQuery)
			return
		}
	}

	ctx, cancel := boundedRead(r)
	defer cancel()
	reader := h.reader
	if reader == nil {
		reader = (*Explorer)(nil)
	}
	page, err := reader.ReadErrors(ctx, mux.Vars(r)["APP_ID"], ErrorsQuery{
		ExplorerQuery: base, Search: search, Fatality: fatality, Sort: sort,
		Limit: limit, Offset: offset, IncludeSeries: includeSeries,
	})
	if err != nil {
		if !isErrorsValidationError(err) {
			log.Printf("observe: reading errors failed: %v", err)
		}
		h.renderErrorsQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, page)
}

func (h *ExplorerHandler) GetErrorDetailsHandler(w http.ResponseWriter, r *http.Request) {
	if !h.requireErrorsLicense(w) {
		return
	}
	values := r.URL.Query()
	query := ErrorDetailsQuery{Global: values.Get("scope") == "all"}
	if scope := values.Get("scope"); scope != "" && scope != "all" {
		handlers.RenderError(w, http.StatusBadRequest, "'scope' must be all or omitted.")
		return
	}
	if query.Global {
		if raw := values.Get("asOf"); raw != "" {
			asOf, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil || asOf.Before(time.Unix(0, 0)) || asOf.After(time.Now()) {
				handlers.RenderError(w, http.StatusBadRequest, "'asOf' must be a past RFC3339 timestamp.")
				return
			}
			query.AsOf = asOf.UTC()
		}
	} else {
		base, err := h.parseBaseQuery(r, ErrorsMaxWindow)
		if err != nil {
			h.renderQueryError(w, err)
			return
		}
		if len(base.Conditions) > 0 {
			h.renderErrorsQueryError(w, ErrInvalidErrorsQuery)
			return
		}
		fatality, ok := parseErrorFatality(values.Get("fatality"))
		if !ok {
			handlers.RenderError(w, http.StatusBadRequest, "'fatality' must be all, fatal or non_fatal.")
			return
		}
		query.ExplorerQuery, query.Fatality = base, fatality
	}
	limit, err := parseErrorLimit(r.URL.Query().Get("limit"))
	if err != nil {
		h.renderErrorsQueryError(w, err)
		return
	}
	if len(r.URL.Query().Get("cursor")) > 512 {
		handlers.RenderError(w, http.StatusBadRequest, "'cursor' is invalid.")
		return
	}
	cursor, err := DecodeLogCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		handlers.RenderError(w, http.StatusBadRequest, "'cursor' is invalid.")
		return
	}
	if query.Global && cursor != nil && query.AsOf.IsZero() {
		handlers.RenderError(w, http.StatusBadRequest, "'asOf' is required when paging all occurrences.")
		return
	}
	query.Limit, query.Cursor = limit, cursor

	ctx, cancel := boundedRead(r)
	defer cancel()
	reader := h.reader
	if reader == nil {
		reader = (*Explorer)(nil)
	}
	details, err := reader.ReadErrorDetails(ctx, mux.Vars(r)["APP_ID"], mux.Vars(r)["ERROR_ID"], query)
	if err != nil {
		if !isErrorsValidationError(err) {
			log.Printf("observe: reading error details failed: %v", err)
		}
		h.renderErrorsQueryError(w, err)
		return
	}
	handlers.RenderJSON(w, http.StatusOK, details)
}
