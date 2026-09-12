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
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"go.openpit.dev/officer/framework/domain"
)

// DecodeBody decodes exactly one non-null JSON value into dst, rejecting any
// member the target does not declare. Malformed JSON is a 400 problem; a valid
// JSON value that does not satisfy the request schema is a 422 problem. It
// reports whether the handler may continue.
//
// Strictness is the point: a control-plane mutation that silently drops an
// unknown member acknowledges an intent it did not carry out - "blocked": true
// answered with 201 Created and an unblocked entity. Leniency stays acceptable
// for reads; for a mutation the acknowledged effect must be the requested one.
func DecodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	return decodeBody(w, r, dst, false)
}

// DecodeBodyAllowUnknownFields decodes exactly one non-null JSON value into
// dst while accepting object members the target does not declare. Use it only
// for forward-compatible import formats, such as portable backup archives,
// where another version may legitimately add fields. Control-plane mutations
// must use DecodeBody so the server never acknowledges ignored intent.
func DecodeBodyAllowUnknownFields(
	w http.ResponseWriter, r *http.Request, dst any,
) bool {
	return decodeBody(w, r, dst, true)
}

// ValidJSONUnicode reports whether raw uses valid UTF-8 and every JSON \u
// escape that denotes a UTF-16 surrogate belongs to a valid pair. It does not
// validate the remaining JSON syntax.
func ValidJSONUnicode(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		if raw[index] >= utf8.RuneSelf {
			_, size := utf8.DecodeRune(raw[index:])
			if size == 1 {
				return false
			}
			index += size - 1
			continue
		}
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			index++
			if index >= len(raw) {
				return false
			}
			if raw[index] != 'u' {
				continue
			}
			first, ok := jsonUTF16CodeUnit(raw[index+1:])
			if !ok {
				return false
			}
			index += 4
			if first >= 0xdc00 && first <= 0xdfff {
				return false
			}
			if first < 0xd800 || first > 0xdbff {
				continue
			}
			if index+6 >= len(raw) ||
				raw[index+1] != '\\' || raw[index+2] != 'u' {
				return false
			}
			second, ok := jsonUTF16CodeUnit(raw[index+3:])
			if !ok || second < 0xdc00 || second > 0xdfff {
				return false
			}
			index += 6
		}
	}
	return true
}

func jsonUTF16CodeUnit(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var codeUnit uint16
	for _, char := range raw[:4] {
		codeUnit <<= 4
		switch {
		case char >= '0' && char <= '9':
			codeUnit += uint16(char - '0')
		case char >= 'a' && char <= 'f':
			codeUnit += uint16(char-'a') + 10
		case char >= 'A' && char <= 'F':
			codeUnit += uint16(char-'A') + 10
		default:
			return 0, false
		}
	}
	return codeUnit, true
}

func decodeBody(
	w http.ResponseWriter, r *http.Request, dst any, allowUnknownFields bool,
) bool {
	decoder := json.NewDecoder(r.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		WriteBadRequestProblem(w, decodeBodyMessage(err), "malformed_json")
		return false
	}
	if !ValidJSONUnicode(raw) {
		WriteBadRequestProblem(w, "invalid JSON", "malformed_json")
		return false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		WriteValidationProblem(w, "request body must not be null", "", "non_null")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		WriteBadRequestProblem(w, "invalid JSON", "malformed_json")
		return false
	}
	valueDecoder := json.NewDecoder(bytes.NewReader(raw))
	if !allowUnknownFields {
		valueDecoder.DisallowUnknownFields()
	}
	if err := valueDecoder.Decode(dst); err != nil {
		detail, pointer, constraint := decodeBodyValidation(err, raw, dst)
		WriteValidationProblem(w, detail, pointer, constraint)
		return false
	}
	return true
}

func decodeBodyValidation(err error, raw []byte, dst any) (string, string, string) {
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		return "request member has an invalid type", jsonPointer(typeErr.Field), "type"
	}
	const unknownFieldPrefix = "json: unknown field "
	msg := err.Error()
	if strings.HasPrefix(msg, unknownFieldPrefix) {
		field := strings.Trim(strings.TrimPrefix(msg, unknownFieldPrefix), `"`)
		if utf8.RuneCountInString(field) <= 64 {
			return "unknown field in request body",
				unknownJSONPointer(raw, dst, field), "unknown_field"
		}
		return "unknown field in request body", "", "unknown_field"
	}
	return "request body does not match the schema", "", "schema"
}

func unknownJSONPointer(raw []byte, dst any, field string) string {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return jsonPointer(field)
	}
	matches := make([][]string, 0, 2)
	findUnknownJSONFields(value, reflect.TypeOf(dst), field, nil, &matches)
	if len(matches) == 0 {
		return jsonPointer(field)
	}
	if len(matches) != 1 {
		return ""
	}
	pointer := jsonPointerSegments(matches[0])
	if len(pointer) > 1024 {
		return ""
	}
	return pointer
}

func findUnknownJSONFields(
	value any,
	target reflect.Type,
	field string,
	path []string,
	matches *[][]string,
) {
	if len(*matches) > 1 || len(path) >= 32 {
		return
	}
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		fields := jsonStructFields(target)
		for name, child := range object {
			fieldType, exists := fields[name]
			if !exists {
				if name == field {
					*matches = append(*matches, appendPath(path, name))
				}
				continue
			}
			findUnknownJSONFields(
				child, fieldType, field, appendPath(path, name), matches,
			)
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return
		}
		for index, child := range array {
			findUnknownJSONFields(
				child, target.Elem(), field,
				appendPath(path, strconv.Itoa(index)), matches,
			)
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return
		}
		for name, child := range object {
			findUnknownJSONFields(
				child, target.Elem(), field, appendPath(path, name), matches,
			)
		}
	}
}

func jsonStructFields(target reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for index := 0; index < target.NumField(); index++ {
		field := target.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if tag == "-" {
			continue
		}
		if field.Anonymous && tag == "" {
			embedded := field.Type
			for embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				for name, fieldType := range jsonStructFields(embedded) {
					fields[name] = fieldType
				}
				continue
			}
		}
		if tag == "" {
			tag = field.Name
		}
		fields[tag] = field.Type
	}
	return fields
}

func appendPath(path []string, segment string) []string {
	result := make([]string, len(path), len(path)+1)
	copy(result, path)
	return append(result, segment)
}

func jsonPointer(field string) string {
	if field == "" {
		return ""
	}
	return jsonPointerSegments(strings.Split(field, "."))
}

func jsonPointerSegments(parts []string) string {
	for index, part := range parts {
		part = strings.ReplaceAll(part, "~", "~0")
		parts[index] = strings.ReplaceAll(part, "/", "~1")
	}
	return "/" + strings.Join(parts, "/")
}

// decodeBodyMessage names the offending member for an unknown-field rejection,
// but only within a length bound, and keeps the opaque "invalid JSON" wording
// for every other decode failure. Nothing but the member name is ever echoed:
// the name is caller-controlled text that ends up in an operator's error toast,
// so a body carrying a megabyte-long member is refused without repeating it.
func decodeBodyMessage(err error) string {
	const unknownFieldPrefix = "json: unknown field "
	msg := err.Error()
	if !strings.HasPrefix(msg, unknownFieldPrefix) {
		return "invalid JSON"
	}
	quoted := strings.TrimPrefix(msg, unknownFieldPrefix)
	if utf8.RuneCountInString(strings.Trim(quoted, `"`)) > 64 {
		return "unknown field in request body"
	}
	return "unknown field " + quoted
}

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

// PathID reads the decoded {id} chi path parameter.
func PathID(r *http.Request) (string, error) {
	return pathString(r, "id", "invalid URL encoding in id")
}

// PathGroupCode reads the decoded {code} chi path parameter.
func PathGroupCode(r *http.Request) (string, error) {
	return pathString(r, "code", "invalid URL encoding in code")
}

// PathCommand reads the decoded {command} chi path parameter.
func PathCommand(r *http.Request) (string, error) {
	return pathString(r, "command", "invalid URL encoding in command")
}

// PathOrderExternalID reads the decoded order {id} path parameter.
func PathOrderExternalID(r *http.Request) (string, error) {
	return pathString(r, "id", "invalid URL encoding in order id")
}

// PathOrderEventID reads the decoded {eventId} chi path parameter.
func PathOrderEventID(r *http.Request) (string, error) {
	return pathString(r, "eventId", "invalid URL encoding in order event id")
}

// PathSigningKeyID reads the decoded {keyId} chi path parameter.
func PathSigningKeyID(r *http.Request) (string, error) {
	return pathString(r, "keyId", "invalid URL encoding in signing key id")
}

// PathAccountID reads the decoded {code} chi path parameter.
func PathAccountID(r *http.Request) (domain.AccountID, error) {
	decoded, err := pathString(r, "code", "invalid URL encoding in account code")
	return domain.AccountID(decoded), err
}

func pathString(r *http.Request, key, msg string) (string, error) {
	raw := chi.URLParam(r, key)
	// Chi reads Path directly unless RawPath preserves a non-canonical escape.
	if r.URL.RawPath == "" {
		return raw, nil
	}
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
		WriteValidationErr(w, err)
	case errors.Is(err, domain.ErrForbidden):
		WriteErrMsg(w, http.StatusForbidden, "forbidden", domain.ErrForbidden.Error())
	// A rejected missing account is matched before the generic not-found case so
	// it keeps its own code and its structured account field.
	case errors.Is(err, domain.ErrAccountMissing):
		writeAccountMissingErr(w, err)
	case errors.Is(err, domain.ErrNotFound):
		WriteErrMsg(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrAlreadyExists):
		WriteErrMsg(w, http.StatusConflict, "conflict", err.Error())
	case errors.Is(err, domain.ErrHasDependents):
		writeHasDependentsErr(w, err)
	case errors.Is(err, domain.ErrTerminalOrder):
		WriteErrMsg(w, http.StatusConflict, "terminal_order", domain.ErrTerminalOrder.Error())
	case errors.Is(err, domain.ErrExecutionReportRequired):
		WriteErrMsg(w, http.StatusConflict, "execution_report_required", err.Error())
	case isCurrencyChangeBlocked(err):
		writeCurrencyChangeBlockedErr(w, err)
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

// ProblemError identifies one invalid request member. Pointer is a JSON
// Pointer; the empty string addresses the request as a whole.
type ProblemError struct {
	Code       string `json:"code"`
	Constraint string `json:"constraint,omitempty"`
	Pointer    string `json:"pointer"`
}

// ProblemDetails is the RFC 9457 error representation used for request
// validation failures.
type ProblemDetails struct {
	Type   string         `json:"type"`
	Title  string         `json:"title"`
	Status int            `json:"status"`
	Detail string         `json:"detail"`
	Errors []ProblemError `json:"errors"`
}

// WriteBadRequestProblem reports malformed HTTP or JSON syntax.
func WriteBadRequestProblem(w http.ResponseWriter, detail, constraint string) {
	writeProblem(w, http.StatusBadRequest, detail, ProblemError{
		Code:       "bad_request",
		Constraint: constraint,
		Pointer:    "",
	})
}

// WriteValidationProblem reports a syntactically valid request that violates
// the request schema or a parameter constraint.
func WriteValidationProblem(w http.ResponseWriter, detail, pointer, constraint string) {
	writeProblem(w, http.StatusUnprocessableEntity, detail, ProblemError{
		Code:       "validation",
		Constraint: constraint,
		Pointer:    pointer,
	})
}

func writeProblem(w http.ResponseWriter, status int, detail string, item ProblemError) {
	title := http.StatusText(status)
	if status == http.StatusUnprocessableEntity {
		title = "Unprocessable Content"
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(ProblemDetails{
		Type:   "about:blank",
		Title:  title,
		Status: status,
		Detail: detail,
		Errors: []ProblemError{item},
	}); err != nil {
		slog.Error("write problem response", "error", err)
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

// writeAccountMissingErr reports a request that named an unknown account and
// asked to reject it. The account code travels as its own envelope field so a
// client matches on structure rather than on the human-readable message.
func writeAccountMissingErr(w http.ResponseWriter, err error) {
	var typed domain.AccountMissingError
	if !errors.As(err, &typed) {
		typed = domain.AccountMissingError{}
	}
	WriteJSON(w, http.StatusNotFound, map[string]any{
		"error": map[string]any{
			"code":    "account_missing",
			"message": err.Error(),
			"account": typed.Account.String(),
		},
	})
}

func isCurrencyChangeBlocked(err error) bool {
	var typed domain.CurrencyChangeBlockedError
	return errors.As(err, &typed)
}

func writeCurrencyChangeBlockedErr(w http.ResponseWriter, err error) {
	var typed domain.CurrencyChangeBlockedError
	if !errors.As(err, &typed) {
		WriteErrMsg(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	errorBody := map[string]any{
		"code":    "currency_change_blocked",
		"message": err.Error(),
	}
	switch typed.Scope {
	case domain.ScopeAccount:
		errorBody["field"] = "currency"
		errorBody["account"] = typed.TargetID
		errorBody["path"] = "accounts." + typed.TargetID + ".currency"
		errorBody["constraint"] = "economically_empty"
	case domain.ScopeAccountGroup:
		errorBody["field"] = "currency"
		errorBody["group"] = typed.TargetID
		errorBody["path"] = "groups." + typed.TargetID + ".currency"
		errorBody["constraint"] = "all_members_economically_empty"
	default:
		WriteErrMsg(w, http.StatusConflict, "conflict", err.Error())
		return
	}
	if errors.Is(typed, domain.ErrCurrencyValuedLimit) {
		errorBody["constraint"] = "currency_valued_limit"
	}
	WriteJSON(w, http.StatusConflict, map[string]any{"error": errorBody})
}

// WriteValidationErr writes a syntactically valid request constraint failure.
func WriteValidationErr(w http.ResponseWriter, err error) {
	WriteValidationProblem(
		w,
		err.Error(),
		domain.ValidationPointer(err),
		domain.ValidationConstraint(err),
	)
}

// WriteErrMsg writes the legacy error envelope for non-validation errors. It
// always honors the status and code chosen by the caller.
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
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json response", "error", err)
	}
}
