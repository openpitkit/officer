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
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
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

// PublicError is the form of an error that a client sees. WriteErr and the
// MCP tools both report errors through DescribeErr, so one error carries the
// same code and message on both surfaces.
type PublicError struct {
	// Status is the HTTP status of the REST response.
	Status int
	// Code is the stable wire code a client matches on.
	Code string
	// Message is the client-visible text. It never carries the cause of an
	// upstream or internal failure.
	Message string
}

// DescribeErr maps err to its public form. It is the one ordered
// classification of domain sentinels and typed domain errors, shared by
// WriteErr and the MCP tools; the first matching case wins. A domain error
// keeps its message: its sentinel text where wrapper text must not reach the
// client, its full text otherwise. An upstream provider failure maps to
// upstream with a neutral message and its cause is logged as a warning; any
// other error maps to internal with "internal error" and its cause is logged
// as an error. Callers do not log err again.
func DescribeErr(err error) PublicError {
	var currencyBlocked domain.CurrencyChangeBlockedError
	switch {
	case errors.Is(err, domain.ErrTooLarge):
		return PublicError{http.StatusRequestEntityTooLarge, "too_large", err.Error()}
	case errors.Is(err, domain.ErrInvalid):
		return PublicError{http.StatusUnprocessableEntity, "validation", err.Error()}
	case errors.Is(err, domain.ErrLiveTradingUnavailable):
		return PublicError{http.StatusForbidden, "live_trading_unavailable", domain.ErrLiveTradingUnavailable.Error()}
	case errors.Is(err, domain.ErrTradingModeMismatch):
		return PublicError{http.StatusConflict, "trading_mode_mismatch", domain.ErrTradingModeMismatch.Error()}
	case errors.Is(err, domain.ErrFeeAccountRequired):
		return PublicError{http.StatusConflict, "fee_account_required", domain.ErrFeeAccountRequired.Error()}
	case errors.Is(err, domain.ErrAccountsNotManaged):
		return PublicError{http.StatusConflict, "accounts_not_managed", err.Error()}
	case errors.Is(err, domain.ErrVenueOrderManaged):
		return PublicError{http.StatusConflict, "venue_order_managed", domain.ErrVenueOrderManaged.Error()}
	case errors.Is(err, domain.ErrNoDestination):
		return PublicError{http.StatusConflict, "no_destination", domain.ErrNoDestination.Error()}
	case errors.Is(err, domain.ErrDestinationAmbiguous):
		return PublicError{http.StatusConflict, "destination_ambiguous", domain.ErrDestinationAmbiguous.Error()}
	case errors.Is(err, domain.ErrForbidden):
		return PublicError{http.StatusForbidden, "forbidden", domain.ErrForbidden.Error()}
	// A rejected missing account is matched before the generic not-found case so
	// it keeps its own code and its structured account field.
	case errors.Is(err, domain.ErrAccountMissing):
		return PublicError{http.StatusNotFound, "account_missing", err.Error()}
	case errors.As(err, new(domain.AssetMissingError)):
		return PublicError{http.StatusNotFound, "asset_missing", err.Error()}
	case errors.Is(err, domain.ErrNotFound):
		return PublicError{http.StatusNotFound, "not_found", err.Error()}
	case errors.Is(err, domain.ErrAlreadyExists):
		return PublicError{http.StatusConflict, "conflict", err.Error()}
	case errors.Is(err, domain.ErrHasDependents):
		return PublicError{http.StatusConflict, "has_dependents", domain.ErrHasDependents.Error()}
	case errors.Is(err, domain.ErrTerminalOrder):
		return PublicError{http.StatusConflict, "terminal_order", domain.ErrTerminalOrder.Error()}
	case errors.Is(err, domain.ErrExecutionReportRequired):
		return PublicError{http.StatusConflict, "execution_report_required", err.Error()}
	case errors.Is(err, domain.ErrApprovalRequired):
		return PublicError{http.StatusConflict, "approval_required", err.Error()}
	case errors.As(err, &currencyBlocked):
		// Only these scopes have a structured target on the wire; any other
		// scope reports a plain conflict.
		switch currencyBlocked.Scope {
		case domain.ScopeAccount, domain.ScopeAccountGroup:
			return PublicError{http.StatusConflict, "currency_change_blocked", err.Error()}
		}
		return PublicError{http.StatusConflict, "conflict", err.Error()}
	case errors.Is(err, domain.ErrConflict):
		return PublicError{http.StatusConflict, "conflict", err.Error()}
	case errors.Is(err, domain.ErrEngineRestarting):
		return PublicError{http.StatusServiceUnavailable, "engine_restarting", err.Error()}
	case errors.Is(err, domain.ErrNotImplemented):
		return PublicError{http.StatusNotImplemented, "not_implemented", err.Error()}
	case errors.Is(err, domain.ErrUpstream):
		slog.Warn("upstream provider request failed", "error", err)
		return PublicError{http.StatusBadGateway, "upstream",
			"The provider couldn't complete the request. " +
				"Check your provider access and the request, then try again."}
	default:
		slog.Error("unhandled internal error serving request", "error", err)
		return PublicError{http.StatusInternalServerError, "internal", "internal error"}
	}
}

// WriteErr writes the response that DescribeErr maps err to. A code that
// carries structured members on REST gets them next to its code and message:
// the RFC 9457 problem body for validation, candidates for
// destination_ambiguous, account for account_missing, asset for
// asset_missing, dependents for has_dependents and the blocked target for
// currency_change_blocked.
func WriteErr(w http.ResponseWriter, err error) {
	public := DescribeErr(err)
	switch public.Code {
	case "validation":
		WriteValidationErr(w, err)
	case "destination_ambiguous":
		writeDestinationAmbiguousErr(w, public, err)
	case "account_missing":
		writeAccountMissingErr(w, public, err)
	case "asset_missing":
		// DescribeErr reports this code only when err holds the typed error.
		var assetMissing domain.AssetMissingError
		errors.As(err, &assetMissing)
		WriteJSON(w, public.Status, map[string]any{
			"error": map[string]any{
				"code":    public.Code,
				"message": public.Message,
				"asset":   assetMissing.Asset,
			},
		})
	case "has_dependents":
		writeHasDependentsErr(w, public, err)
	case "currency_change_blocked":
		writeCurrencyChangeBlockedErr(w, public, err)
	default:
		WriteErrMsg(w, public.Status, public.Code, public.Message)
	}
}

// TradingDestinationDTO is the wire representation shared by destination lists
// and destination-ambiguous errors.
type TradingDestinationDTO struct {
	Connection   string             `json:"connection"`
	Label        string             `json:"label"`
	Provider     string             `json:"provider"`
	Mode         domain.TradingMode `json:"mode"`
	Enabled      bool               `json:"enabled"`
	VenueAccount string             `json:"venueAccount"`
	State        string             `json:"state"`
	Error        string             `json:"error,omitempty"`
}

// ToTradingDestinationDTOs encodes destinations with the error the backend
// reports. An empty input produces an empty JSON array.
func ToTradingDestinationDTOs(in []backend.TradingDestinationStatus) []TradingDestinationDTO {
	out := make([]TradingDestinationDTO, 0, len(in))
	for _, d := range in {
		out = append(out, TradingDestinationDTO{
			Connection: d.Connection.String(), Label: d.Label, Provider: d.Provider,
			Mode: d.Mode, Enabled: d.Enabled, VenueAccount: d.VenueAccount,
			State: d.State, Error: d.Error,
		})
	}
	return out
}

// TradingDTO is the wire representation of the trading runtime state, shared
// by the REST and MCP reports of it.
type TradingDTO struct {
	Providers   []TradingProviderDTO   `json:"providers"`
	Connections []TradingConnectionDTO `json:"connections"`
}

// TradingProviderDTO is one trading provider and the capabilities it offers.
type TradingProviderDTO struct {
	Type            string `json:"type"`
	Title           string `json:"title"`
	VenueAccounts   bool   `json:"venueAccounts"`
	VerifiesSymbols bool   `json:"verifiesSymbols"`
	SearchesSymbols bool   `json:"searchesSymbols"`
	UnlinkedFees    bool   `json:"unlinkedFees"`
}

// TradingConnectionDTO is one trading connection with its runtime state, the
// presence of its secrets, its instruments and its grants.
type TradingConnectionDTO struct {
	ID            string                 `json:"id"`
	Provider      string                 `json:"provider"`
	Label         string                 `json:"label"`
	Mode          domain.TradingMode     `json:"mode"`
	Enabled       bool                   `json:"enabled"`
	VenueIdentity string                 `json:"venueIdentity"`
	FeeAccount    string                 `json:"feeAccount"`
	State         string                 `json:"state"`
	Error         string                 `json:"error,omitempty"`
	Diagnostics   []DiagnosticDTO        `json:"diagnostics"`
	Secrets       map[string]bool        `json:"secrets"`
	Instruments   []TradingInstrumentDTO `json:"instruments"`
	Access        []TradingAccessDTO     `json:"access"`
}

// TradingInstrumentDTO is one instrument of a trading connection.
type TradingInstrumentDTO struct {
	ExternalSymbol string `json:"externalSymbol"`
	BaseAsset      string `json:"baseAsset"`
	QuoteAsset     string `json:"quoteAsset"`
	Enabled        bool   `json:"enabled"`
}

// TradingAccessDTO is one grant of a connection. Exactly one of Account and
// AccountGroup names the subject; Group is the granted account's current group
// membership, empty for a group grant and for an account outside any group.
type TradingAccessDTO struct {
	Account      string `json:"account"`
	AccountGroup string `json:"accountGroup"`
	VenueAccount string `json:"venueAccount"`
	Group        string `json:"group"`
}

// ToTradingDTO encodes the trading runtime state. Empty provider and
// connection lists produce empty JSON arrays.
func ToTradingDTO(status backend.TradingStatus) TradingDTO {
	out := TradingDTO{
		Providers:   make([]TradingProviderDTO, 0, len(status.Providers)),
		Connections: make([]TradingConnectionDTO, 0, len(status.Connections)),
	}
	for _, p := range status.Providers {
		out.Providers = append(out.Providers, TradingProviderDTO{
			Type: p.Type, Title: p.Title, VenueAccounts: p.VenueAccounts,
			VerifiesSymbols: p.VerifiesSymbols, SearchesSymbols: p.SearchesSymbols,
			UnlinkedFees: p.UnlinkedFees,
		})
	}
	for _, c := range status.Connections {
		out.Connections = append(out.Connections, ToTradingConnectionDTO(c))
	}
	return out
}

// ToTradingConnectionDTO encodes one trading connection with the error and
// diagnostics the backend reports; no diagnostics give an empty JSON array.
func ToTradingConnectionDTO(c backend.TradingConnectionStatus) TradingConnectionDTO {
	out := TradingConnectionDTO{
		ID: c.ExternalID.String(), Provider: c.Provider, Label: c.Label,
		Mode: c.Mode, Enabled: c.Enabled, VenueIdentity: c.VenueIdentity,
		FeeAccount: c.FeeAccount.String(), State: c.State, Error: c.Error,
		Diagnostics: ToDiagnosticDTOs(c.Diagnostics),
		Secrets:     make(map[string]bool, len(c.Secrets)),
		Instruments: make([]TradingInstrumentDTO, 0, len(c.Instruments)),
		Access:      make([]TradingAccessDTO, 0, len(c.Access)),
	}
	for key, present := range c.Secrets {
		out.Secrets[key] = present
	}
	for _, i := range c.Instruments {
		out.Instruments = append(out.Instruments, TradingInstrumentDTO{
			ExternalSymbol: i.ExternalSymbol, BaseAsset: i.BaseAsset,
			QuoteAsset: i.QuoteAsset, Enabled: i.Enabled,
		})
	}
	for _, row := range c.Access {
		out.Access = append(out.Access, TradingAccessDTO{
			Account: row.Access.Account.String(), AccountGroup: row.Access.AccountGroup,
			VenueAccount: row.Access.VenueAccount, Group: row.Group,
		})
	}
	return out
}

// DiagnosticActionDTO is one operator action a diagnostic offers.
type DiagnosticActionDTO struct {
	Type   string `json:"type"`
	Target string `json:"target,omitempty"`
}

// DiagnosticDTO is the wire representation of one health diagnostic.
type DiagnosticDTO struct {
	Level       string                `json:"level"`
	Code        string                `json:"code"`
	Kind        string                `json:"kind"`
	Title       string                `json:"title"`
	Detail      string                `json:"detail"`
	Remediation string                `json:"remediation,omitempty"`
	Instrument  string                `json:"instrument,omitempty"`
	Actions     []DiagnosticActionDTO `json:"actions,omitempty"`
	At          string                `json:"at"`
}

// ToDiagnosticDTOs encodes diagnostics. No diagnostics produce an empty
// non-nil slice; a diagnostic without actions carries no actions member.
func ToDiagnosticDTOs(diags []health.Diagnostic) []DiagnosticDTO {
	out := make([]DiagnosticDTO, 0, len(diags))
	for _, diag := range diags {
		var actions []DiagnosticActionDTO
		if len(diag.Actions) > 0 {
			actions = make([]DiagnosticActionDTO, 0, len(diag.Actions))
			for _, a := range diag.Actions {
				actions = append(actions, DiagnosticActionDTO{
					Type:   a.Type,
					Target: a.Target,
				})
			}
		}
		out = append(out, DiagnosticDTO{
			Level:       diag.Level,
			Code:        diag.Code,
			Kind:        diag.Kind,
			Title:       diag.Title,
			Detail:      diag.Detail,
			Remediation: diag.Remediation,
			Instrument:  diag.Instrument,
			Actions:     actions,
			At:          diag.At.Format(time.RFC3339Nano),
		})
	}
	return out
}

// CredentialStorageDTO is the wire representation of how stored credentials
// sit at rest, shared by the REST and MCP reports of it. It never carries a
// credential or a key name, and all three flags are always present.
type CredentialStorageDTO struct {
	CredentialsSealed       bool `json:"credentialsSealed"`
	PlaintextTradingKeys    bool `json:"plaintextTradingKeys"`
	PlaintextMarketDataKeys bool `json:"plaintextMarketDataKeys"`
}

// ToCredentialStorageDTO encodes a credential storage report.
func ToCredentialStorageDTO(storage backend.CredentialStorage) CredentialStorageDTO {
	return CredentialStorageDTO{
		CredentialsSealed:       storage.Sealed,
		PlaintextTradingKeys:    storage.PlaintextTradingKeys,
		PlaintextMarketDataKeys: storage.PlaintextMarketDataKeys,
	}
}

func writeDestinationAmbiguousErr(w http.ResponseWriter, public PublicError, err error) {
	var destinations []backend.TradingDestinationStatus
	var typed *backend.DestinationAmbiguousError
	if errors.As(err, &typed) {
		destinations = typed.Candidates
	}
	WriteJSON(w, public.Status, map[string]any{
		"error": map[string]any{
			"code": public.Code, "message": public.Message,
			"candidates": ToTradingDestinationDTOs(destinations),
		},
	})
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

func writeHasDependentsErr(w http.ResponseWriter, public PublicError, err error) {
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
	WriteJSON(w, public.Status, map[string]any{
		"error": map[string]any{
			"code":       public.Code,
			"message":    public.Message,
			"dependents": dependents,
		},
	})
}

// writeAccountMissingErr reports a request that named an unknown account and
// asked to reject it. The account code travels as its own envelope field so a
// client matches on structure rather than on the human-readable message.
func writeAccountMissingErr(w http.ResponseWriter, public PublicError, err error) {
	var typed domain.AccountMissingError
	if !errors.As(err, &typed) {
		typed = domain.AccountMissingError{}
	}
	WriteJSON(w, public.Status, map[string]any{
		"error": map[string]any{
			"code":    public.Code,
			"message": public.Message,
			"account": typed.Account.String(),
		},
	})
}

// writeCurrencyChangeBlockedErr adds the blocked target to the error body.
// DescribeErr reports currency_change_blocked only for a typed
// CurrencyChangeBlockedError of the account or account group scope.
func writeCurrencyChangeBlockedErr(w http.ResponseWriter, public PublicError, err error) {
	var typed domain.CurrencyChangeBlockedError
	errors.As(err, &typed)
	errorBody := map[string]any{
		"code":    public.Code,
		"message": public.Message,
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
	}
	if errors.Is(typed, domain.ErrCurrencyValuedLimit) {
		errorBody["constraint"] = "currency_valued_limit"
	}
	WriteJSON(w, public.Status, map[string]any{"error": errorBody})
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
