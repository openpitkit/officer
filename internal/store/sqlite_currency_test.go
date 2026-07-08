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

package store

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestSQLiteAccountCurrencyCascadeAndAssetDependents(t *testing.T) {
	ctx := context.Background()
	_, rs := newTestStore(t)

	for _, asset := range []string{"USD", "EUR", "JPY"} {
		if err := rs.CreateAsset(ctx, domain.Asset{Code: asset}); err != nil {
			t.Fatalf("CreateAsset(%s): %v", asset, err)
		}
	}
	if err := rs.SetGroupCurrency(ctx, "", "USD"); err != nil {
		t.Fatalf("SetGroupCurrency(default): %v", err)
	}
	if _, err := rs.CreateGroup(ctx, domain.AccountGroup{
		Code:     "desk-a",
		Currency: "EUR",
	}); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code:      "account-tier",
		GroupCode: "desk-a",
		Currency:  "JPY",
	}); err != nil {
		t.Fatalf("CreateAccount account-tier: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{
		Code:      "group-tier",
		GroupCode: "desk-a",
	}); err != nil {
		t.Fatalf("CreateAccount group-tier: %v", err)
	}
	if _, err := rs.CreateAccount(ctx, domain.Account{Code: "default-tier"}); err != nil {
		t.Fatalf("CreateAccount default-tier: %v", err)
	}

	accountTier, _, err := rs.GetAccount(ctx, "account-tier")
	if err != nil {
		t.Fatalf("GetAccount account-tier: %v", err)
	}
	if accountTier.EffectiveCurrency != "JPY" ||
		accountTier.CurrencyOrigin != domain.CurrencyOriginAccount ||
		accountTier.GroupCurrency != "EUR" ||
		accountTier.DefaultCurrency != "USD" {
		t.Fatalf("account-tier cascade = %+v", accountTier)
	}
	groupTier, _, err := rs.GetAccount(ctx, "group-tier")
	if err != nil {
		t.Fatalf("GetAccount group-tier: %v", err)
	}
	if groupTier.EffectiveCurrency != "EUR" ||
		groupTier.CurrencyOrigin != domain.CurrencyOriginGroup {
		t.Fatalf("group-tier cascade = %+v", groupTier)
	}
	defaultTier, _, err := rs.GetAccount(ctx, "default-tier")
	if err != nil {
		t.Fatalf("GetAccount default-tier: %v", err)
	}
	if defaultTier.EffectiveCurrency != "USD" ||
		defaultTier.CurrencyOrigin != domain.CurrencyOriginDefault {
		t.Fatalf("default-tier cascade = %+v", defaultTier)
	}

	if err := rs.DeleteAsset(ctx, "USD", true); !errors.Is(err, domain.ErrHasDependents) {
		t.Fatalf("DeleteAsset(USD) = %v, want ErrHasDependents", err)
	}
	if err := rs.SetAccountCurrency(ctx, "account-tier", "GBP"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SetAccountCurrency unknown = %v, want ErrInvalid", err)
	}
}
