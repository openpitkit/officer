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

// Package businesscsv owns Pit Officer's business-entity CSV and ZIP wire
// format. It deliberately stays independent of HTTP so backend tests can drive
// the same schemas through real service/store paths.
package businesscsv

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// Entity identifies one business CSV schema.
type Entity string

const (
	EntityAccountGroups Entity = "account_groups"
	EntityAccounts      Entity = "accounts"
	EntityPositions     Entity = "positions"
	EntityOrders        Entity = "orders"
	EntityTrades        Entity = "trades"
)

// Delimiter is the operator-selected CSV delimiter.
type Delimiter string

const (
	DelimiterComma     Delimiter = "comma"
	DelimiterSemicolon Delimiter = "semicolon"
	DelimiterTab       Delimiter = "tab"
	DelimiterPipe      Delimiter = "pipe"
)

// ConflictPolicy controls how import applies rows whose key already exists.
type ConflictPolicy string

const (
	ConflictSkip    ConflictPolicy = "skip"
	ConflictReplace ConflictPolicy = "replace"
	ConflictStop    ConflictPolicy = "stop"
)

// MaxImportBytes is the hard server-side cap for decoded import payloads and
// decompressed ZIP content.
const MaxImportBytes = 128 << 20

// ExportFilter carries the filters supported by the current UI tables.
type ExportFilter struct {
	GroupCode    string
	Account      domain.AccountID
	Asset        string
	Source       domain.Source
	GroupCodeSet bool
}

// ExportFile is a CSV or ZIP payload ready for an HTTP response.
type ExportFile struct {
	Name        string
	ContentType string
	Body        []byte
}

// ImportFile is the decoded CSV body selected from a plain upload or ZIP.
type ImportFile struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Body []byte `json:"-"`
}

// Conflict identifies one row that intersects existing business state.
type Conflict struct {
	Key string `json:"key"`
	Row int    `json:"row"`
}

// ImportCounts summarises an import run.
type ImportCounts struct {
	Rows      int  `json:"rows"`
	Applied   int  `json:"applied"`
	Skipped   int  `json:"skipped"`
	Conflicts int  `json:"conflicts"`
	Stopped   bool `json:"stopped"`
}

// GroupRow is one account_groups CSV row. Code is the immutable group code;
// Title is the mutable display name.
type GroupRow struct {
	Code        string
	Title       string
	Currency    string
	Notes       string
	BlockReason string
	Blocked     bool
}

// AccountRow is one accounts CSV row. Code is the immutable account code; Title
// is the mutable display name; GroupCode links to the account's group by its
// code (empty = no group).
type AccountRow struct {
	Code        domain.AccountID
	Title       string
	GroupCode   string
	Currency    string
	Notes       string
	BlockReason string
	Blocked     bool
}

// PositionRow is one positions CSV row. Account is the account code; Asset is
// the asset symbol (code).
type PositionRow struct {
	Account           domain.AccountID
	Asset             string
	Available         string
	Held              string
	Incoming          string
	RealizedPnl       string
	AverageEntryPrice string
}

// ImportRows carries parsed rows for exactly one importable entity.
type ImportRows struct {
	Groups    []GroupRow
	Accounts  []AccountRow
	Positions []PositionRow
}

// NewTooLargeError returns the stable import-size error surfaced to API
// callers. decompressed selects the ZIP extraction variant.
func NewTooLargeError(decompressed bool) error {
	prefix := "import"
	if decompressed {
		prefix = "decompressed import"
	}
	return tooLargeError{message: fmt.Sprintf(
		"%s exceeds the maximum size of %d MiB; split the export into smaller files or use the API for bulk loading",
		prefix, MaxImportBytes>>20,
	)}
}

type tooLargeError struct {
	message string
}

func (e tooLargeError) Error() string { return e.message }

func (e tooLargeError) Unwrap() error { return domain.ErrTooLarge }

// Column headers. All human-facing identity fields use code or symbol, never a
// surrogate id. Orders and trades use external_id; trades reference their order
// via order_external_id.
var (
	groupHeader   = []string{"code", "title", "currency", "notes", "blocked", "block_reason"}
	accountHeader = []string{
		"code", "title", "group_code", "currency", "notes", "blocked",
		"block_reason",
	}
	legacyGroupHeader = []string{
		"code", "title", "notes", "blocked", "block_reason",
	}
	legacyAccountHeader = []string{
		"code", "title", "group_code", "notes", "blocked", "block_reason",
	}
	positionHeader = []string{
		"account_code", "asset", "available", "held", "incoming",
		"realized_pnl", "average_entry_price",
	}
	orderHeader = []string{
		"external_id", "at", "account_code", "source", "principal",
		"base_asset", "quote_asset", "side", "amount_kind", "amount_value",
		"price", "status",
	}
	tradeHeader = []string{
		"external_id", "order_external_id", "at", "account_code", "source",
		"principal", "base_asset", "quote_asset", "side", "quantity", "price",
		"lock_price", "commission_amount", "commission_currency",
	}
)

// ValidateEntity returns an error when entity is not supported for export.
func ValidateEntity(entity Entity) error {
	switch entity {
	case EntityAccountGroups, EntityAccounts, EntityPositions, EntityOrders,
		EntityTrades:
		return nil
	default:
		return fmt.Errorf("unknown business CSV entity %q: %w", entity, domain.ErrInvalid)
	}
}

// ValidateImportEntity returns an error when entity is not importable.
func ValidateImportEntity(entity Entity) error {
	switch entity {
	case EntityAccountGroups, EntityAccounts, EntityPositions:
		return nil
	default:
		return fmt.Errorf("business CSV entity %q is not importable: %w", entity, domain.ErrInvalid)
	}
}

// Rune resolves the delimiter to encoding/csv's comma rune.
func (d Delimiter) Rune() (rune, error) {
	switch d {
	case "", DelimiterComma:
		return ',', nil
	case DelimiterSemicolon:
		return ';', nil
	case DelimiterTab:
		return '\t', nil
	case DelimiterPipe:
		return '|', nil
	default:
		return 0, fmt.Errorf("unknown CSV delimiter %q: %w", d, domain.ErrInvalid)
	}
}

// ValidateConflictPolicy returns an error when policy is unknown.
func ValidateConflictPolicy(policy ConflictPolicy) error {
	switch policy {
	case ConflictSkip, ConflictReplace, ConflictStop:
		return nil
	default:
		return fmt.Errorf("unknown CSV conflict policy %q: %w", policy, domain.ErrInvalid)
	}
}

// EncodeGroups serializes account groups. The identity columns are code and
// title; never a surrogate id.
func EncodeGroups(rows []domain.AccountGroup, delimiter Delimiter) ([]byte, error) {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, groupHeader)
	for _, row := range rows {
		out = append(out, []string{
			row.Code, row.Title, row.Currency, row.Notes,
			formatBool(row.Blocked), row.BlockReason,
		})
	}
	return writeCSV(out, delimiter)
}

// EncodeAccounts serializes accounts. Account identity is code, the display name
// is title; group link is group_code (never a surrogate id).
func EncodeAccounts(rows []domain.Account, delimiter Delimiter) ([]byte, error) {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, accountHeader)
	for _, row := range rows {
		out = append(out, []string{
			row.Code.String(), row.Title, row.GroupCode, row.Currency,
			row.Notes, formatBool(row.Blocked), row.BlockReason,
		})
	}
	return writeCSV(out, delimiter)
}

// EncodePositions serializes spot-funds positions. Account and asset are
// identified by code/symbol; no surrogate id.
func EncodePositions(rows []domain.Balance, delimiter Delimiter) ([]byte, error) {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, positionHeader)
	for _, row := range rows {
		out = append(out, []string{
			string(row.Account), row.Asset, row.Available, row.Held,
			row.Incoming, row.RealizedPnl, row.AverageEntryPrice,
		})
	}
	return writeCSV(out, delimiter)
}

// EncodeOrders serializes orders for export. Orders are export-only and are
// addressed by external_id; no integer surrogate id is emitted. The account
// column carries the account code; assets carry their symbols.
func EncodeOrders(rows []domain.Order, delimiter Delimiter) ([]byte, error) {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, orderHeader)
	for _, row := range rows {
		out = append(out, []string{
			row.ExternalID.String(), row.At.Format(time.RFC3339Nano),
			string(row.Account), string(row.Source), row.Principal,
			row.BaseAsset, row.QuoteAsset, string(row.Side),
			string(row.AmountKind), row.AmountValue, row.Price,
			string(row.Status),
		})
	}
	return writeCSV(out, delimiter)
}

// EncodeTrades serializes trades for export. Trades are export-only and are
// addressed by external_id; the originating order is referenced by
// order_external_id. No integer id is emitted.
func EncodeTrades(rows []domain.Trade, delimiter Delimiter) ([]byte, error) {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, tradeHeader)
	for _, row := range rows {
		out = append(out, []string{
			row.ExternalID.String(), row.Order.String(),
			row.At.Format(time.RFC3339Nano), string(row.Account),
			string(row.Source), row.Principal, row.BaseAsset, row.QuoteAsset,
			string(row.Side), row.Quantity, row.Price, row.LockPrice,
			commissionAmount(row.Commission), commissionCurrency(row.Commission),
		})
	}
	return writeCSV(out, delimiter)
}

func commissionAmount(c *domain.Commission) string {
	if c == nil {
		return ""
	}
	return c.Amount
}

func commissionCurrency(c *domain.Commission) string {
	if c == nil {
		return ""
	}
	return c.Currency
}

// ParseImport parses an importable entity CSV body.
func ParseImport(entity Entity, body []byte, delimiter Delimiter) (ImportRows, error) {
	if err := ValidateImportEntity(entity); err != nil {
		return ImportRows{}, err
	}
	records, err := readCSV(body, delimiter)
	if err != nil {
		return ImportRows{}, err
	}
	if len(records) == 0 {
		return ImportRows{}, fmt.Errorf("CSV is empty: %w", domain.ErrInvalid)
	}
	switch entity {
	case EntityAccountGroups:
		return parseGroups(records)
	case EntityAccounts:
		return parseAccounts(records)
	case EntityPositions:
		return parsePositions(records)
	default:
		return ImportRows{}, fmt.Errorf("business CSV entity %q is not importable: %w", entity, domain.ErrInvalid)
	}
}

// DecodeImportFile selects a plain CSV body or the one real file in a ZIP.
func DecodeImportFile(filename string, payload []byte) (ImportFile, error) {
	if strings.EqualFold(path.Ext(filename), ".zip") || isZipPayload(payload) {
		return decodeZipWithLimit(payload, MaxImportBytes)
	}
	return ImportFile{Name: filename, Type: "csv", Body: payload}, nil
}

// WrapExport optionally wraps csvBody into a ZIP file using best compression.
func WrapExport(
	entity Entity, csvBody []byte, zipFile bool, now time.Time,
) (ExportFile, error) {
	base := fmt.Sprintf(
		"pit-officer-%s-%s.csv", entity, now.UTC().Format("20060102T150405Z"),
	)
	if !zipFile {
		return ExportFile{Name: base, ContentType: "text/csv; charset=utf-8", Body: csvBody}, nil
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(w, flate.BestCompression)
	})
	h := &zip.FileHeader{Name: base, Method: zip.Deflate}
	w, err := zw.CreateHeader(h)
	if err != nil {
		return ExportFile{}, fmt.Errorf("create business CSV zip entry: %w", err)
	}
	if _, err := w.Write(csvBody); err != nil {
		return ExportFile{}, fmt.Errorf("write business CSV zip entry: %w", err)
	}
	if err := zw.Close(); err != nil {
		return ExportFile{}, fmt.Errorf("close business CSV zip: %w", err)
	}
	return ExportFile{
		Name:        strings.TrimSuffix(base, ".csv") + ".zip",
		ContentType: "application/zip",
		Body:        buf.Bytes(),
	}, nil
}

func writeCSV(records [][]string, delimiter Delimiter) ([]byte, error) {
	comma, err := delimiter.Rune()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = comma
	if err := w.WriteAll(records); err != nil {
		return nil, fmt.Errorf("write CSV: %w", err)
	}
	return buf.Bytes(), nil
}

func readCSV(body []byte, delimiter Delimiter) ([][]string, error) {
	comma, err := delimiter.Rune()
	if err != nil {
		return nil, err
	}
	r := csv.NewReader(bytes.NewReader(body))
	r.Comma = comma
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read CSV: %w: %w", err, domain.ErrInvalid)
	}
	return records, nil
}

func parseGroups(records [][]string) (ImportRows, error) {
	header := groupHeader
	legacy := false
	if err := requireHeader(records[0], groupHeader); err != nil {
		if legacyErr := requireHeader(records[0], legacyGroupHeader); legacyErr != nil {
			return ImportRows{}, err
		}
		header = legacyGroupHeader
		legacy = true
	}
	rows := make([]GroupRow, 0, len(records)-1)
	for i, rec := range records[1:] {
		if len(rec) != len(header) {
			return ImportRows{}, rowErr(i+2, "wrong field count")
		}
		currency := ""
		notesIndex := 2
		blockedIndex := 3
		reasonIndex := 4
		if !legacy {
			currency = strings.TrimSpace(rec[2])
			notesIndex = 3
			blockedIndex = 4
			reasonIndex = 5
		}
		blocked, err := parseBool(rec[blockedIndex], i+2)
		if err != nil {
			return ImportRows{}, err
		}
		code := strings.TrimSpace(rec[0])
		if code == "" {
			return ImportRows{}, rowErr(i+2, "code is empty")
		}
		rows = append(rows, GroupRow{
			Code: code, Title: rec[1], Currency: currency,
			Notes: rec[notesIndex], Blocked: blocked,
			BlockReason: rec[reasonIndex],
		})
	}
	return ImportRows{Groups: rows}, nil
}

func parseAccounts(records [][]string) (ImportRows, error) {
	header := accountHeader
	legacy := false
	if err := requireHeader(records[0], accountHeader); err != nil {
		if legacyErr := requireHeader(records[0], legacyAccountHeader); legacyErr != nil {
			return ImportRows{}, err
		}
		header = legacyAccountHeader
		legacy = true
	}
	rows := make([]AccountRow, 0, len(records)-1)
	for i, rec := range records[1:] {
		if len(rec) != len(header) {
			return ImportRows{}, rowErr(i+2, "wrong field count")
		}
		currency := ""
		notesIndex := 3
		blockedIndex := 4
		reasonIndex := 5
		if !legacy {
			currency = strings.TrimSpace(rec[3])
			notesIndex = 4
			blockedIndex = 5
			reasonIndex = 6
		}
		blocked, err := parseBool(rec[blockedIndex], i+2)
		if err != nil {
			return ImportRows{}, err
		}
		code := strings.TrimSpace(rec[0])
		if code == "" {
			return ImportRows{}, rowErr(i+2, "code is empty")
		}
		rows = append(rows, AccountRow{
			Code:      domain.AccountID(code),
			Title:     rec[1],
			GroupCode: strings.TrimSpace(rec[2]),
			Currency:  currency,
			Notes:     rec[notesIndex],
			Blocked:   blocked, BlockReason: rec[reasonIndex],
		})
	}
	return ImportRows{Accounts: rows}, nil
}

func parsePositions(records [][]string) (ImportRows, error) {
	if err := requireHeader(records[0], positionHeader); err != nil {
		return ImportRows{}, err
	}
	rows := make([]PositionRow, 0, len(records)-1)
	for i, rec := range records[1:] {
		if len(rec) != len(positionHeader) {
			return ImportRows{}, rowErr(i+2, "wrong field count")
		}
		accountCode := strings.TrimSpace(rec[0])
		if accountCode == "" {
			return ImportRows{}, rowErr(i+2, "account_code is empty")
		}
		assetCode := strings.TrimSpace(rec[1])
		if assetCode == "" {
			return ImportRows{}, rowErr(i+2, "asset is empty")
		}
		rows = append(rows, PositionRow{
			Account: domain.AccountID(accountCode),
			Asset:   assetCode, Available: rec[2], Held: rec[3],
			Incoming: rec[4], RealizedPnl: rec[5], AverageEntryPrice: rec[6],
		})
	}
	return ImportRows{Positions: rows}, nil
}

func requireHeader(got, want []string) error {
	if len(got) != len(want) {
		return fmt.Errorf("CSV header mismatch: %w", domain.ErrInvalid)
	}
	for i := range want {
		if strings.TrimSpace(got[i]) != want[i] {
			return fmt.Errorf(
				"CSV header column %d = %q, want %q: %w",
				i+1, got[i], want[i], domain.ErrInvalid,
			)
		}
	}
	return nil
}

func decodeZipWithLimit(payload []byte, maxBytes int64) (ImportFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		return ImportFile{}, fmt.Errorf("invalid business CSV zip archive: %w: %w", err, domain.ErrInvalid)
	}
	var realFiles []*zip.File
	var declared uint64
	maxDeclared := uint64(maxBytes)
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || ignoredZipPath(f.Name) {
			continue
		}
		if f.UncompressedSize64 > maxDeclared ||
			declared > maxDeclared-f.UncompressedSize64 {
			return ImportFile{}, NewTooLargeError(true)
		}
		declared += f.UncompressedSize64
		realFiles = append(realFiles, f)
	}
	if len(realFiles) == 0 {
		return ImportFile{}, fmt.Errorf("business CSV zip contains no real files: %w", domain.ErrInvalid)
	}
	if len(realFiles) > 1 {
		return ImportFile{}, fmt.Errorf("business CSV zip contains multiple real files: %w", domain.ErrInvalid)
	}
	rc, err := realFiles[0].Open()
	if err != nil {
		return ImportFile{}, fmt.Errorf("open business CSV zip entry: %w", err)
	}
	defer func() { _ = rc.Close() }()
	body, err := readLimitedImportContent(rc, maxBytes)
	if err != nil {
		if errors.Is(err, domain.ErrTooLarge) {
			return ImportFile{}, err
		}
		return ImportFile{}, fmt.Errorf("read business CSV zip entry: %w", err)
	}
	return ImportFile{Name: realFiles[0].Name, Type: "zip", Body: body}, nil
}

func readLimitedImportContent(r io.Reader, maxBytes int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, NewTooLargeError(true)
	}
	return body, nil
}

func ignoredZipPath(name string) bool {
	clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
	for _, part := range strings.Split(clean, "/") {
		if part == "." || part == "" {
			continue
		}
		if strings.HasPrefix(part, ".") || part == "__MACOSX" {
			return true
		}
	}
	return false
}

func isZipPayload(raw []byte) bool {
	return len(raw) >= 4 &&
		raw[0] == 'P' &&
		raw[1] == 'K' &&
		raw[2] == 0x03 &&
		raw[3] == 0x04
}

func parseBool(s string, row int) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, rowErr(row, fmt.Sprintf("invalid bool %q", s))
	}
}

func formatBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func rowErr(row int, msg string) error {
	return fmt.Errorf("CSV row %d: %s: %w", row, msg, domain.ErrInvalid)
}
