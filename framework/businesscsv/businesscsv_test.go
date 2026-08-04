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
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
)

func TestEncodeBusinessEntities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		encode     func() ([]byte, error)
		wantHeader string
		wantRow    string
	}{
		{
			name: "groups",
			encode: func() ([]byte, error) {
				return businesscsv.EncodeGroups([]domain.AccountGroup{{
					Code: "desk-a", Title: "Desk A", Currency: "USD",
					Notes: "note", Blocked: true, BlockReason: "hold",
				}}, businesscsv.DelimiterComma)
			},
			wantHeader: "code,title,currency,notes,blocked,block_reason",
			wantRow:    "desk-a,Desk A,USD,note,true,hold",
		},
		{
			name: "accounts",
			encode: func() ([]byte, error) {
				return businesscsv.EncodeAccounts([]domain.Account{{
					Code: "acc-1", Title: "Account 1", GroupCode: "desk-a",
					Currency: "USD", Pnl: "12.5", Notes: "note",
				}}, businesscsv.DelimiterComma)
			},
			wantHeader: "code,title,group_code,currency,pnl,pnl_halt_reason,notes,blocked,block_reason",
			wantRow:    "acc-1,Account 1,desk-a,USD,12.5,,note,false,",
		},
		{
			name: "positions",
			encode: func() ([]byte, error) {
				return businesscsv.EncodePositions([]domain.Balance{{
					Account: "acc-1", Asset: "AAPL", Available: "10",
					Held: "2", Incoming: "1", RealizedPnl: "3.5",
					AverageEntryPrice: "150",
				}}, businesscsv.DelimiterComma)
			},
			wantHeader: "account_code,asset,available,held,incoming,realized_pnl,realized_pnl_halt_reason,average_entry_price",
			wantRow:    "acc-1,AAPL,10,2,1,3.5,,150",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body, err := tc.encode()
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			content := strings.ReplaceAll(string(body), "\r\n", "\n")
			if !strings.HasPrefix(content, tc.wantHeader+"\n") ||
				!strings.Contains(content, tc.wantRow+"\n") {
				t.Fatalf("CSV =\n%s", content)
			}
		})
	}
}

func TestEncodeOrdersIncludesRestoredLockPrice(t *testing.T) {
	t.Parallel()
	id := mustExternalID(t, "AAAAAAAAAAAAAAAAAAAAAA")
	body, err := businesscsv.EncodeOrders([]domain.Order{{
		ExternalID: id,
		At:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Account:    "acc-1", Source: domain.SourcePanel,
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		AmountKind: domain.OrderAmountKindQuantity, AmountValue: "10",
		Price: "150", Leaves: "7", Status: domain.OrderStatusAccepted,
	}}, businesscsv.DelimiterComma, func(order domain.Order) string {
		if order.ExternalID != id {
			t.Fatalf("lock price order = %q, want %q", order.ExternalID, id)
		}
		return "149.5"
	})
	if err != nil {
		t.Fatalf("EncodeOrders: %v", err)
	}
	content := string(body)
	if !strings.Contains(content, "price,lock_price,leaves,status") ||
		!strings.Contains(content, ",150,149.5,7,accepted,false") {
		t.Fatalf("orders CSV misses lock price:\n%s", content)
	}
}

func TestEncodeTradesUsesPublicIDs(t *testing.T) {
	t.Parallel()
	id := mustExternalID(t, "BBBBBBBBBBBBBBBBBBBBBB")
	orderID := mustExternalID(t, "CCCCCCCCCCCCCCCCCCCCCC")
	body, err := businesscsv.EncodeTrades([]domain.Trade{{
		ExternalID: id, Order: orderID,
		At:      time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
		Account: "acc-1", Source: domain.SourcePanel,
		BaseAsset: "AAPL", QuoteAsset: "USD", Side: domain.OrderSideBuy,
		Quantity: "10", Price: "151", LockPrice: "150",
	}}, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodeTrades: %v", err)
	}
	content := string(body)
	if !strings.HasPrefix(content, "id,order_id,at,account_code,") ||
		!strings.Contains(content, id.String()) ||
		!strings.Contains(content, orderID.String()) {
		t.Fatalf("trades CSV =\n%s", content)
	}
}

func TestExportNeutralizesFormulasButKeepsNegativeNumbers(t *testing.T) {
	t.Parallel()
	groups, err := businesscsv.EncodeGroups([]domain.AccountGroup{{
		Code: "desk-a", Title: "=SUM(A1)", Notes: "'=quoted",
	}}, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodeGroups: %v", err)
	}
	if !strings.Contains(string(groups), "'=SUM(A1)") ||
		!strings.Contains(string(groups), "''=quoted") {
		t.Fatalf("formula fields were not neutralized:\n%s", groups)
	}
	positions, err := businesscsv.EncodePositions([]domain.Balance{{
		Account: "acc-1", Asset: "USD", Available: "-100", RealizedPnl: "-5.5",
	}}, businesscsv.DelimiterComma)
	if err != nil {
		t.Fatalf("EncodePositions: %v", err)
	}
	if strings.Contains(string(positions), "'-100") ||
		strings.Contains(string(positions), "'-5.5") {
		t.Fatalf("negative decimals must remain numeric:\n%s", positions)
	}
}

func TestExportDelimiters(t *testing.T) {
	t.Parallel()
	for _, delimiter := range []businesscsv.Delimiter{
		businesscsv.DelimiterComma,
		businesscsv.DelimiterSemicolon,
		businesscsv.DelimiterTab,
		businesscsv.DelimiterPipe,
	} {
		delimiter := delimiter
		t.Run(string(delimiter), func(t *testing.T) {
			t.Parallel()
			sep, err := delimiter.Rune()
			if err != nil {
				t.Fatalf("Rune: %v", err)
			}
			body, err := businesscsv.EncodeAccounts(
				[]domain.Account{{Code: "acc-1", Title: "Account 1"}}, delimiter,
			)
			if err != nil {
				t.Fatalf("EncodeAccounts: %v", err)
			}
			if !strings.ContainsRune(strings.SplitN(string(body), "\n", 2)[0], sep) {
				t.Fatalf("header does not use %q: %s", sep, body)
			}
		})
	}
}

func TestWrapExportZip(t *testing.T) {
	t.Parallel()
	file, err := businesscsv.WrapExport(
		businesscsv.EntityAccounts,
		[]byte("code,title\n"),
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

func mustExternalID(t *testing.T, value string) domain.ExternalID {
	t.Helper()
	id, err := domain.ParseExternalID(value)
	if err != nil {
		t.Fatalf("ParseExternalID(%q): %v", value, err)
	}
	return id
}
