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
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

// oneAccountFeatures is a composition that fixes one account, so clients do
// not see accounts and an omitted account is the service's to supply.
var oneAccountFeatures = backend.AccountFeatures{Account: "default"}

// TestOmittedAccountByAccountFeatures covers every transport site that refuses
// an omitted account before the service sees it. When the composition does not
// offer accounts, the request reaches the service with the account omitted, or
// named as the composition's account where the service reads an empty one as a
// clear (the fee account). With the default features each site still refuses
// at /account, required, without calling the service.
func TestOmittedAccountByAccountFeatures(t *testing.T) {
	order := `"baseAsset":"AAPL","quoteAsset":"USD","side":"buy",` +
		`"amountKind":"quantity","amountValue":"1","price":"100"`
	for _, tc := range []struct {
		name, method, path, body string
		// status is the answer once the request reaches the service.
		status int
		// want is the account the service receives under oneAccountFeatures.
		want domain.AccountID
		// refusal is the default composition's validation detail.
		refusal string
		// reached reports the account the service received and whether it
		// was called at all.
		reached func(*fakeService) (domain.AccountID, bool)
	}{
		{
			"orders.submit", http.MethodPost, "/orders/submit",
			`{` + order + `,"mode":"immediate"}`, http.StatusCreated, "",
			"account id is empty",
			func(f *fakeService) (domain.AccountID, bool) {
				return f.submitOrderIn.Account, f.submitTokenMode != ""
			},
		},
		{
			"orders.drop-copy.submit", http.MethodPost, "/orders/drop-copy/submit",
			`{` + order + `}`, http.StatusCreated, "", "account id is empty",
			func(f *fakeService) (domain.AccountID, bool) {
				return f.submitOrderIn.Account, f.submitOrderIn.DropCopy
			},
		},
		{
			"orders.send-paper", http.MethodPost, "/orders/send-paper",
			`{` + order + `,"destination":{"route":{}}}`, http.StatusCreated, "",
			"account id is empty",
			func(f *fakeService) (domain.AccountID, bool) {
				return f.submitOrderIn.Account, slices.Contains(f.tradingCalls, "send-paper")
			},
		},
		{
			"trading.access.put", http.MethodPut, tradingPath + "/access",
			`{"venueAccount":"sub-1"}`, http.StatusOK, "",
			"account or accountGroup is required",
			func(f *fakeService) (domain.AccountID, bool) {
				return f.tradingAccess.Account, slices.Contains(f.tradingCalls, "access")
			},
		},
		{
			"trading.access.delete", http.MethodDelete, tradingPath + "/access",
			"", http.StatusNoContent, "", "account or accountGroup is required",
			func(f *fakeService) (domain.AccountID, bool) {
				return f.tradingAccount, slices.Contains(f.tradingCalls, "delete-access")
			},
		},
		{
			"trading.fee-account.put", http.MethodPut, tradingPath + "/fee-account",
			`{}`, http.StatusOK, oneAccountFeatures.Account, "account is required",
			func(f *fakeService) (domain.AccountID, bool) {
				return f.tradingAccount, slices.Contains(f.tradingCalls, "fee-account")
			},
		},
	} {
		t.Run(tc.name+"/one-account", func(t *testing.T) {
			svc := tradingFixture(t)
			svc.accountFeatures = oneAccountFeatures
			rec := callTrading(t, svc, "/api/v1", tc.method, tc.path, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tc.status, rec.Body)
			}
			got, called := tc.reached(svc)
			if !called || got != tc.want {
				t.Fatalf("service called %v with account %q, want called with %q",
					called, got, tc.want)
			}
		})
		t.Run(tc.name+"/default", func(t *testing.T) {
			svc := tradingFixture(t)
			rec := callTrading(t, svc, "/api/v1", tc.method, tc.path, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body)
			}
			body := bodyMap(t, rec.Result())
			problems, _ := body["errors"].([]any)
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want one", problems)
			}
			problem := problems[0].(map[string]any)
			if problem["pointer"] != "/account" || problem["constraint"] != "required" {
				t.Fatalf("problem = %v, want required at /account", problem)
			}
			if detail, _ := body["detail"].(string); !strings.Contains(detail, tc.refusal) {
				t.Fatalf("detail = %q, want it to contain %q", detail, tc.refusal)
			}
			if _, called := tc.reached(svc); called {
				t.Fatal("request without an account reached the service")
			}
		})
	}
}

// TestOmittedLimitAccountReadsBackStoredBarrier covers the limit puts, which
// answer from a read-back of the stored barrier: an account-axis barrier that
// names no account reaches the service as sent and is read back under the
// composition's one account, which the service's account scope stored it with.
// A barrier whose scope has no account axis is read back with its empty account.
func TestOmittedLimitAccountReadsBackStoredBarrier(t *testing.T) {
	stored := node.AccountLimits{
		RateLimits: []domain.LimitRate{{
			Scope: domain.ScopeAccount, Account: oneAccountFeatures.Account,
			Window: time.Second, MaxOrders: 100,
		}, {
			Scope: domain.ScopeBroker, Window: time.Second, MaxOrders: 50,
		}},
		OrderSizeLimits: []domain.LimitOrderSize{{
			Scope: domain.ScopeAccountUnderlyingAsset, Account: oneAccountFeatures.Account,
			Asset: "AAPL", MaxQuantity: "10",
		}},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{{
			Scope: domain.ScopeAccount, Account: oneAccountFeatures.Account,
			Currency: "USD", LowerBound: "-100",
		}},
	}
	for _, tc := range []struct {
		name, path, body, member string
		sent                     func(*fakeService) domain.AccountID
		// want is the account of the barrier the read-back answers with.
		want domain.AccountID
	}{
		{
			"rate", "/limits/rate",
			`{"scope":"account","windowMs":1000,"maxOrders":100}`, "rateLimit",
			func(f *fakeService) domain.AccountID { return f.rateLimitPut.Account },
			oneAccountFeatures.Account,
		},
		{
			"rate-broker", "/limits/rate",
			`{"scope":"broker","windowMs":1000,"maxOrders":50}`, "rateLimit",
			func(f *fakeService) domain.AccountID { return f.rateLimitPut.Account },
			"",
		},
		{
			"order-size", "/limits/order-size",
			`{"scope":"account_underlying_asset","asset":"AAPL","maxQuantity":"10"}`,
			"orderSizeLimit",
			func(f *fakeService) domain.AccountID { return f.orderSizeLimitPut.Account },
			oneAccountFeatures.Account,
		},
		{
			"spot-funds-pnl-bounds", "/limits/spot-funds-pnl-bounds",
			`{"scope":"account","currency":"USD","lowerBound":"-100"}`,
			"spotFundsPnlBoundsLimit",
			func(f *fakeService) domain.AccountID {
				return f.spotFundsPnlBoundsLimitPut.Account
			},
			oneAccountFeatures.Account,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeService{accountFeatures: oneAccountFeatures, limits: stored}
			rec := callTrading(t, svc, "/api/v1", http.MethodPut, tc.path, tc.body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
			}
			if sent := tc.sent(svc); sent != "" {
				t.Fatalf("service received account %q, want it omitted", sent)
			}
			limit, _ := bodyMap(t, rec.Result())[tc.member].(map[string]any)
			if limit["account"] != string(tc.want) {
				t.Fatalf("read-back %s = %v, want the stored barrier of %q",
					tc.member, limit, tc.want)
			}
		})
	}
}

// TestAccountsNotManagedIsConflict covers a refused change of the account set
// through a real handler: the service's domain.ErrAccountsNotManaged answers
// 409 with the accounts_not_managed code and the error's own text.
func TestAccountsNotManagedIsConflict(t *testing.T) {
	refusal := fmt.Errorf("backend: %w", domain.ErrAccountsNotManaged)
	svc := &fakeService{createErr: refusal}
	rec := callTrading(t, svc, "/api/v1", http.MethodPost, "/accounts", `{"code":"acc-9"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body)
	}
	errObj, _ := bodyMap(t, rec.Result())["error"].(map[string]any)
	if errObj["code"] != "accounts_not_managed" || errObj["message"] != refusal.Error() {
		t.Fatalf("error = %v, want accounts_not_managed", errObj)
	}
}
