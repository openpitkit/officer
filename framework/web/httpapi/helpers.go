// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Please see https://openpit.dev and the OWNERS file for details.

package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/go-chi/chi/v5"
	"go.openpit.dev/officer/framework/domain"
)

// LimitParam reads the ?limit= query parameter, defaulting to def and capping
// at capN. A non-positive or non-integer value is an error.
func LimitParam(r *http.Request, def, capN int) (int, error) {
	n := def
	if s := r.URL.Query().Get("limit"); s != "" {
		v, err := strconv.Atoi(s)
		if err != nil || v <= 0 {
			return 0, errors.New("limit must be a positive integer")
		}
		n = v
	}
	if n > capN {
		n = capN
	}
	return n, nil
}

// PathID reads and URL-decodes the {id} chi path parameter.
func PathID(r *http.Request) (string, error) {
	return pathString(r, "id", "invalid URL encoding in id")
}

// PathGroupCode reads and URL-decodes the {code} chi path parameter.
func PathGroupCode(r *http.Request) (string, error) {
	return pathString(r, "code", "invalid URL encoding in code")
}

// PathCommand reads and URL-decodes the {command} chi path parameter.
func PathCommand(r *http.Request) (string, error) {
	return pathString(r, "command", "invalid URL encoding in command")
}

// PathOrderExternalID reads and URL-decodes the {externalId} chi path parameter.
func PathOrderExternalID(r *http.Request) (string, error) {
	return pathString(r, "externalId", "invalid URL encoding in order external id")
}

// PathOrderEventID reads and URL-decodes the {eventId} chi path parameter.
func PathOrderEventID(r *http.Request) (string, error) {
	return pathString(r, "eventId", "invalid URL encoding in order event id")
}

// PathSigningKeyID reads and URL-decodes the {keyId} chi path parameter.
func PathSigningKeyID(r *http.Request) (string, error) {
	return pathString(r, "keyId", "invalid URL encoding in signing key id")
}

// PathAccountID reads and URL-decodes the {code} chi path parameter.
func PathAccountID(r *http.Request) (domain.AccountID, error) {
	decoded, err := pathString(r, "code", "invalid URL encoding in account code")
	return domain.AccountID(decoded), err
}

func pathString(r *http.Request, key, msg string) (string, error) {
	raw := chi.URLParam(r, key)
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", errors.New(msg)
	}
	return decoded, nil
}

// WriteErr maps a domain sentinel error to the appropriate HTTP status and JSON
// error body.
func WriteErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrTooLarge):
		WriteErrMsg(w, http.StatusRequestEntityTooLarge, "too_large", err.Error())
	case errors.Is(err, domain.ErrInvalid):
		WriteErrMsg(w, http.StatusBadRequest, "validation", err.Error())
	case errors.Is(err, domain.ErrForbidden):
		WriteErrMsg(w, http.StatusForbidden, "forbidden", domain.ErrForbidden.Error())
	case errors.Is(err, domain.ErrNotFound):
		WriteErrMsg(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		WriteErrMsg(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrHasDependents):
		writeHasDependentsErr(w, err)
	case errors.Is(err, domain.ErrTerminalOrder):
		WriteErrMsg(w, http.StatusConflict, "terminal_order", domain.ErrTerminalOrder.Error())
	case errors.Is(err, domain.ErrConflict):
		WriteErrMsg(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrEngineRestarting):
		WriteErrMsg(w, http.StatusServiceUnavailable, "engine_restarting", err.Error())
	case errors.Is(err, domain.ErrNotImplemented):
		WriteErrMsg(w, http.StatusNotImplemented, "not_implemented", err.Error())
	case errors.Is(err, domain.ErrUpstream):
		slog.Warn("upstream provider request failed", "error", err)
		WriteErrMsg(w, http.StatusBadGateway, "upstream",
			"The market-data provider couldn't complete the request. "+
				"Check the symbol and your provider access, then try again.")
	default:
		slog.Error("unhandled internal error serving request", "error", err)
		WriteErrMsg(w, http.StatusInternalServerError, "internal", "internal error")
	}
}

func writeHasDependentsErr(w http.ResponseWriter, err error) {
	var typed domain.HasDependentsError
	if !errors.As(err, &typed) {
		typed = domain.HasDependentsError{}
	}
	dependents := make([]map[string]any, 0, len(typed.Dependents))
	for _, dep := range typed.Dependents {
		dependents = append(dependents, map[string]any{
			"kind":  dep.Kind,
			"count": dep.Count,
		})
	}
	WriteJSON(w, http.StatusConflict, map[string]any{
		"error": map[string]any{
			"code":       "has_dependents",
			"message":    domain.ErrHasDependents.Error(),
			"dependents": dependents,
		},
	})
}

// WriteErrMsg writes a JSON error body with the given HTTP status, code and
// message.
func WriteErrMsg(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	})
}

// WriteJSON encodes v as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
