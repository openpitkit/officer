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

package backend

import (
	"slices"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func TestSortBalanceRowsOrdersRealizedPnlExactlyWithEmptyFirst(t *testing.T) {
	t.Parallel()

	rows := []store.BalanceListRow{
		{Balance: domain.Balance{Account: "acc", Asset: "ten", RealizedPnl: "10"}},
		{Balance: domain.Balance{Account: "acc", Asset: "empty"}},
		{Balance: domain.Balance{Account: "acc", Asset: "negative", RealizedPnl: "-5.00"}},
		{Balance: domain.Balance{Account: "acc", Asset: "zero", RealizedPnl: "0"}},
	}

	assets := func() []string {
		got := make([]string, 0, len(rows))
		for _, row := range rows {
			got = append(got, row.Balance.Asset)
		}
		return got
	}

	sortBalanceRows(rows, store.SortSpec{Column: "realizedPnl"})
	if got, want := assets(), []string{"empty", "negative", "zero", "ten"}; !slices.Equal(got, want) {
		t.Fatalf("ascending realized P&L rows = %v, want %v", got, want)
	}

	sortBalanceRows(rows, store.SortSpec{Column: "realizedPnl", Descending: true})
	if got, want := assets(), []string{"empty", "ten", "zero", "negative"}; !slices.Equal(got, want) {
		t.Fatalf("descending realized P&L rows = %v, want %v", got, want)
	}
}
