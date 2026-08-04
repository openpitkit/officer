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
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/shopspring/decimal"
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

// Column headers. All human-facing identity fields use code or symbol, never a
// surrogate id. Orders and trades use id; trades reference their order via
// order_id.
var (
	groupHeader   = []string{"code", "title", "currency", "notes", "blocked", "block_reason"}
	accountHeader = []string{
		"code", "title", "group_code", "currency", "pnl", "pnl_halt_reason", "notes", "blocked",
		"block_reason",
	}
	positionHeader = []string{
		"account_code", "asset", "available", "held", "incoming",
		"realized_pnl", "realized_pnl_halt_reason", "average_entry_price",
	}
	orderHeader = []string{
		"id", "at", "account_code", "source", "principal",
		"base_asset", "quote_asset", "side", "amount_kind", "amount_value",
		"price", "lock_price", "leaves", "status", "drop_copy",
	}
	tradeHeader = []string{
		"id", "order_id", "at", "account_code", "source",
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
			row.Pnl, string(row.PnlHaltReason), row.Notes, formatBool(row.Blocked), row.BlockReason,
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
			row.Incoming, row.RealizedPnl, string(row.RealizedPnlHaltReason), row.AverageEntryPrice,
		})
	}
	return writeCSV(out, delimiter)
}

// OrderLockPrice resolves the display settlement price from an order's opaque
// engine lock.
type OrderLockPrice func(domain.Order) string

// EncodeOrders serializes orders for export. Orders are export-only and are
// addressed by id; no integer surrogate id is emitted. The account column
// carries the account code; assets carry their symbols. lockPrice resolves the
// human-facing price through the engine lock decoder without exposing the lock.
func EncodeOrders(
	rows []domain.Order,
	delimiter Delimiter,
	lockPrice OrderLockPrice,
) ([]byte, error) {
	out := make([][]string, 0, len(rows)+1)
	out = append(out, orderHeader)
	for _, row := range rows {
		price := lockPrice(row)
		out = append(out, []string{
			row.ExternalID.String(), row.At.Format(time.RFC3339Nano),
			string(row.Account), string(row.Source), row.Principal,
			row.BaseAsset, row.QuoteAsset, string(row.Side),
			string(row.AmountKind), row.AmountValue, row.Price, price, row.Leaves,
			string(row.Status), formatBool(row.DropCopy),
		})
	}
	return writeCSV(out, delimiter)
}

// EncodeTrades serializes trades for export. Trades are export-only and are
// addressed by id; the originating order is referenced by order_id. No integer
// surrogate id is emitted.
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

// formulaLeaders are the leading characters a spreadsheet application reads as
// the start of a formula or DDE command. A field beginning with one of them
// executes when the operator opens the export in Excel, LibreOffice or Sheets,
// so the sink neutralizes it. Validation cannot substitute for this guard:
// titles, notes and block reasons are free text and may legitimately start with
// "=".
const formulaLeaders = "=+-@\t\r"

// escapeCSVField neutralizes a spreadsheet formula by prefixing an apostrophe,
// the conventional "treat as text" marker. The prefix stacks on a value that is
// already apostrophe-prefixed so a pre-escaped value remains unambiguous.
//
// A value that parses as a complete decimal number is exempt: a number is never
// a formula in any spreadsheet, and quoting it would turn every negative
// balance and P&L in the export into text the operator cannot sum.
func escapeCSVField(field string) string {
	if !csvFieldIsFormula(field) {
		return field
	}
	return "'" + field
}

func csvFieldIsFormula(field string) bool {
	bare := strings.TrimLeft(field, "'")
	if bare == "" || !strings.ContainsRune(formulaLeaders, rune(bare[0])) {
		return false
	}
	if _, err := decimal.NewFromString(bare); err == nil {
		return false
	}
	return true
}

func writeCSV(records [][]string, delimiter Delimiter) ([]byte, error) {
	comma, err := delimiter.Rune()
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Comma = comma
	for _, record := range records {
		escaped := make([]string, len(record))
		for i, field := range record {
			escaped[i] = escapeCSVField(field)
		}
		if err := w.Write(escaped); err != nil {
			return nil, fmt.Errorf("write CSV: %w", err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, fmt.Errorf("write CSV: %w", err)
	}
	return buf.Bytes(), nil
}

func formatBool(v bool) string {
	if v {
		return "true"
	}
	return "false"
}
