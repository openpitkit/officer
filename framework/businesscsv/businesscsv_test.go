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

package businesscsv_test

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
)

// --- Groups ------------------------------------------------------------------

func TestEncodeDecodeGroups(t *testing.T) {
	t.Parallel()
	groups := []domain.AccountGroup{
		{
			Code: "desk-a", Title: "Desk A", Currency: "USD",
			Notes: "note one", Blocked: false,
		},
		{
			Code: "desk-b", Title: "Desk B", Notes: "",
			Blocked: true, BlockReason: "compliance hold",
		},
	}
	body, err := businesscsv.EncodeGroups(groups, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodeGroups: %v", err)
	}
	rows, err := businesscsv.ParseImport(businesscsv.EntityAccountGroups, body, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(rows.Groups))
	}
	g := rows.Groups[0]
	if g.Code != "desk-a" || g.Title != "Desk A" || g.Currency != "USD" ||
		g.Notes != "note one" || g.Blocked {
		t.Errorf("group[0] = %+v", g)
	}
	g = rows.Groups[1]
	if g.Code != "desk-b" || g.Title != "Desk B" || g.Currency != "" ||
		!g.Blocked || g.BlockReason != "compliance hold" {
		t.Errorf("group[1] = %+v", g)
	}
}

func TestParseGroups_LegacyHeaderDefaultsCurrency(t *testing.T) {
	t.Parallel()
	csv := "code,title,notes,blocked,block_reason\ndesk-a,Desk A,note,false,\n"
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityAccountGroups, []byte(csv), businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Groups) != 1 || rows.Groups[0].Currency != "" ||
		rows.Groups[0].Notes != "note" {
		t.Fatalf("groups = %+v", rows.Groups)
	}
}

func TestParseGroups_EmptyCode(t *testing.T) {
	t.Parallel()
	csv := "code,title,notes,blocked,block_reason\n,Title,note,false,\n"
	_, err := businesscsv.ParseImport(
		businesscsv.EntityAccountGroups, []byte(csv), businesscsv.DelimiterComma,
	)
	if err == nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty code, got %v", err)
	}
}

// --- Accounts ----------------------------------------------------------------

func TestEncodeDecodeAccounts(t *testing.T) {
	t.Parallel()
	accounts := []domain.Account{
		{
			Code: "acc-1", Title: "Desk A One", GroupCode: "desk-a",
			Currency: "USD", Pnl: "12.5", PnlHaltReason: domain.PnlHaltReasonMissingInitialPnl, Notes: "note",
		},
		{Code: "acc-2", GroupCode: "", Notes: "", Blocked: true, BlockReason: "kyc"},
	}
	body, err := businesscsv.EncodeAccounts(accounts, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodeAccounts: %v", err)
	}
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityAccounts, body, businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Accounts) != 2 {
		t.Fatalf("want 2 accounts, got %d", len(rows.Accounts))
	}
	a := rows.Accounts[0]
	if a.Code != "acc-1" || a.Title != "Desk A One" || a.GroupCode != "desk-a" ||
		a.Currency != "USD" || a.Pnl != "12.5" ||
		a.PnlHaltReason != domain.PnlHaltReasonMissingInitialPnl || a.Notes != "note" ||
		a.Blocked || !a.PnlSpecified {
		t.Errorf("account[0] = %+v", a)
	}
	a = rows.Accounts[1]
	if a.Code != "acc-2" || a.Title != "" || a.GroupCode != "" ||
		a.Currency != "" || a.Pnl != "" || !a.Blocked || a.BlockReason != "kyc" ||
		!a.PnlSpecified {
		t.Errorf("account[1] = %+v", a)
	}
}

func TestParseAccounts_LegacyHeaderDefaultsCurrency(t *testing.T) {
	t.Parallel()
	csv := "code,title,group_code,notes,blocked,block_reason\n" +
		"acc-1,Desk A One,desk-a,note,false,\n"
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityAccounts, []byte(csv), businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Accounts) != 1 || rows.Accounts[0].Currency != "" ||
		rows.Accounts[0].Pnl != "0" || rows.Accounts[0].Notes != "note" ||
		rows.Accounts[0].PnlSpecified {
		t.Fatalf("accounts = %+v", rows.Accounts)
	}
}

func TestParseAccounts_PreviousHeaderDefaultsPnl(t *testing.T) {
	t.Parallel()
	csv := "code,title,group_code,currency,notes,blocked,block_reason\n" +
		"acc-1,Desk A One,desk-a,USD,note,false,\n"
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityAccounts, []byte(csv), businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Accounts) != 1 || rows.Accounts[0].Pnl != "0" ||
		rows.Accounts[0].PnlSpecified {
		t.Fatalf("accounts = %+v", rows.Accounts)
	}
}

func TestParseAccounts_PreviousPnlHeaderDefaultsHaltReason(t *testing.T) {
	t.Parallel()
	csv := "code,title,group_code,currency,pnl,notes,blocked,block_reason\n" +
		"acc-1,Desk A One,desk-a,USD,12.5,note,false,\n"
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityAccounts, []byte(csv), businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Accounts) != 1 || rows.Accounts[0].Pnl != "12.5" ||
		rows.Accounts[0].PnlHaltReason != "" || !rows.Accounts[0].PnlSpecified {
		t.Fatalf("accounts = %+v", rows.Accounts)
	}
}

func TestParseAccounts_ExplicitZeroIsSpecified(t *testing.T) {
	t.Parallel()
	csv := "code,title,group_code,currency,pnl,pnl_halt_reason,notes,blocked,block_reason\n" +
		"acc-1,Desk A One,,USD,0,,note,false,\n"
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityAccounts, []byte(csv), businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Accounts) != 1 || rows.Accounts[0].Pnl != "0" ||
		!rows.Accounts[0].PnlSpecified {
		t.Fatalf("accounts = %+v, want explicit zero marked present", rows.Accounts)
	}
}

func TestParseAccounts_NoSurrogateIDColumn(t *testing.T) {
	t.Parallel()
	// Old header had "account_id"; the new header must be "code".
	old := "account_id,group_id,notes,blocked,block_reason\nacc-1,,note,false,\n"
	_, err := businesscsv.ParseImport(
		businesscsv.EntityAccounts, []byte(old), businesscsv.DelimiterComma,
	)
	if err == nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for old header, got %v", err)
	}
}

// --- Positions ---------------------------------------------------------------

func TestEncodeDecodePositions(t *testing.T) {
	t.Parallel()
	balances := []domain.Balance{
		{
			Account: "acc-1", Asset: "AAPL",
			Available: "100", Held: "10", Incoming: "0",
			RealizedPnl: "5.5", RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
			AverageEntryPrice: "150.25",
		},
	}
	body, err := businesscsv.EncodePositions(balances, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodePositions: %v", err)
	}
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityPositions, body, businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Positions) != 1 {
		t.Fatalf("want 1 position, got %d", len(rows.Positions))
	}
	p := rows.Positions[0]
	if p.Account != "acc-1" || p.Asset != "AAPL" || p.Available != "100" ||
		p.RealizedPnl != "5.5" || p.RealizedPnlHaltReason != domain.PnlHaltReasonMissingCostBasis ||
		p.AverageEntryPrice != "150.25" {
		t.Errorf("position = %+v", p)
	}
}

func TestParsePositions_PreviousHeaderDefaultsHaltReason(t *testing.T) {
	t.Parallel()
	csv := "account_code,asset,available,held,incoming,realized_pnl,average_entry_price\n" +
		"acc-1,AAPL,100,10,0,5.5,150.25\n"
	rows, err := businesscsv.ParseImport(
		businesscsv.EntityPositions, []byte(csv), businesscsv.DelimiterComma,
	)
	if err != nil {
		t.Fatalf("ParseImport: %v", err)
	}
	if len(rows.Positions) != 1 || rows.Positions[0].RealizedPnl != "5.5" ||
		rows.Positions[0].RealizedPnlHaltReason != "" ||
		rows.Positions[0].AverageEntryPrice != "150.25" {
		t.Fatalf("positions = %+v", rows.Positions)
	}
}

// --- Orders (export-only) ----------------------------------------------------

func TestEncodeOrdersUsesPublicIDHeader(t *testing.T) {
	t.Parallel()
	xid := mustExternalID(t, "AAAAAAAAAAAAAAAAAAAAAA")
	orders := []domain.Order{
		{
			ExternalID:  xid,
			At:          time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			Account:     "acc-1",
			Source:      domain.SourcePanel,
			Principal:   "op",
			BaseAsset:   "AAPL",
			QuoteAsset:  "USD",
			Side:        domain.OrderSideBuy,
			AmountKind:  domain.OrderAmountKindQuantity,
			AmountValue: "10",
			Price:       "150",
			Status:      domain.OrderStatusAccepted,
		},
	}
	body, err := businesscsv.EncodeOrders(orders, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodeOrders: %v", err)
	}
	content := string(body)
	if !strings.HasPrefix(content, "id,at,account_code,") {
		t.Errorf("orders header does not start with public id column:\n%s", content)
	}
	if strings.Contains(content, "external_id") {
		t.Error("orders header must not expose the internal external_id mnemonic")
	}
	// The public id value must appear in the data row.
	if !strings.Contains(content, xid.String()) {
		t.Errorf("want id %q in row, content:\n%s", xid, content)
	}
}

func TestOrdersNotImportable(t *testing.T) {
	t.Parallel()
	err := businesscsv.ValidateImportEntity(businesscsv.EntityOrders)
	if err == nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("orders must not be importable, got %v", err)
	}
}

// --- Trades (export-only) ----------------------------------------------------

func TestEncodeTradesUsesPublicIDHeaders(t *testing.T) {
	t.Parallel()
	xid := mustExternalID(t, "BBBBBBBBBBBBBBBBBBBBBB")
	orderXID := mustExternalID(t, "CCCCCCCCCCCCCCCCCCCCCC")
	trades := []domain.Trade{
		{
			ExternalID: xid,
			Order:      orderXID,
			At:         time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
			Account:    "acc-1",
			Source:     domain.SourcePanel,
			Principal:  "op",
			BaseAsset:  "AAPL",
			QuoteAsset: "USD",
			Side:       domain.OrderSideBuy,
			Quantity:   "10",
			Price:      "151",
			LockPrice:  "150",
		},
	}
	body, err := businesscsv.EncodeTrades(trades, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodeTrades: %v", err)
	}
	content := string(body)
	if !strings.HasPrefix(content, "id,order_id,at,account_code,") {
		t.Errorf("trades header does not start with public id columns:\n%s", content)
	}
	if strings.Contains(content, "external_id") {
		t.Error("trades header must not expose the internal external_id mnemonic")
	}
	if !strings.Contains(content, xid.String()) {
		t.Errorf("want trade id %q in row", xid)
	}
	if !strings.Contains(content, orderXID.String()) {
		t.Errorf("want order id %q in row", orderXID)
	}
}

func TestTradesNotImportable(t *testing.T) {
	t.Parallel()
	err := businesscsv.ValidateImportEntity(businesscsv.EntityTrades)
	if err == nil || !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("trades must not be importable, got %v", err)
	}
}

// --- Delimiters --------------------------------------------------------------

func TestDelimiterImportExport(t *testing.T) {
	t.Parallel()
	delimiters := []businesscsv.Delimiter{
		businesscsv.DelimiterComma,
		businesscsv.DelimiterSemicolon,
		businesscsv.DelimiterTab,
		businesscsv.DelimiterPipe,
	}
	for _, delimiter := range delimiters {
		t.Run(string(delimiter), func(t *testing.T) {
			body, err := businesscsv.EncodeAccounts([]domain.Account{{
				Code: "acc-1", Title: "Desk A One", GroupCode: "desk-a", Notes: "note",
			}}, delimiter)
			if err != nil {
				t.Fatalf("EncodeAccounts: %v", err)
			}
			rows, err := businesscsv.ParseImport(
				businesscsv.EntityAccounts, body, delimiter,
			)
			if err != nil {
				t.Fatalf("ParseImport: %v", err)
			}
			if len(rows.Accounts) != 1 ||
				rows.Accounts[0].Code != "acc-1" ||
				rows.Accounts[0].Title != "Desk A One" ||
				rows.Accounts[0].GroupCode != "desk-a" {
				t.Fatalf("rows = %+v", rows.Accounts)
			}
		})
	}
}

// --- ZIP handling ------------------------------------------------------------

func TestDecodeImportFileZipEdgeCases(t *testing.T) {
	t.Run("one real file", func(t *testing.T) {
		payload := testZip(t, map[string]string{
			".DS_Store":          "junk",
			"__MACOSX/._acc.csv": "junk",
			"accounts.csv":       "code,title,group_code,notes,blocked,block_reason\n",
		})
		file, err := businesscsv.DecodeImportFile("accounts.zip", payload)
		if err != nil {
			t.Fatalf("DecodeImportFile: %v", err)
		}
		if file.Name != "accounts.csv" || file.Type != "zip" {
			t.Fatalf("file = %+v", file)
		}
	})

	t.Run("multiple real files", func(t *testing.T) {
		payload := testZip(t, map[string]string{
			"a.csv": "",
			"b.csv": "",
		})
		_, err := businesscsv.DecodeImportFile("accounts.zip", payload)
		if err == nil || !strings.Contains(err.Error(), "multiple real files") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("malformed zip", func(t *testing.T) {
		_, err := businesscsv.DecodeImportFile("accounts.zip", []byte("bad"))
		if err == nil || !strings.Contains(err.Error(), "invalid business CSV zip") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestWrapExportZip(t *testing.T) {
	file, err := businesscsv.WrapExport(
		businesscsv.EntityAccounts,
		[]byte("code,title,group_code,notes,blocked,block_reason\n"),
		true,
		time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("WrapExport: %v", err)
	}
	if file.ContentType != "application/zip" ||
		file.Name != "pit-officer-accounts-20260625T100000Z.zip" {
		t.Fatalf("file = %+v", file)
	}
	zr, err := zip.NewReader(bytes.NewReader(file.Body), int64(len(file.Body)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	if len(zr.File) != 1 ||
		zr.File[0].Name != "pit-officer-accounts-20260625T100000Z.csv" ||
		zr.File[0].Method != zip.Deflate {
		t.Fatalf("zip entries = %+v", zr.File)
	}
}

// --- Helpers -----------------------------------------------------------------

func testZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("Create(%s): %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return buf.Bytes()
}

// mustExternalID decodes a 22-char base64url string into a domain.ExternalID
// for test fixtures. Panics on invalid input.
func mustExternalID(t *testing.T, s string) domain.ExternalID {
	t.Helper()
	id, err := domain.ParseExternalID(s)
	if err != nil {
		t.Fatalf("ParseExternalID(%q): %v", s, err)
	}
	return id
}
