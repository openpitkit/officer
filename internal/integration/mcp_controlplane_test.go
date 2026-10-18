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
// Please see https://officer.openpit.dev and the OWNERS file for details.

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// enabledControlPlaneSource is the control-plane Source of a service with every
// command enabled; every other call goes through the real forwarder.
type enabledControlPlaneSource struct {
	frameworkmcp.Source
}

func (enabledControlPlaneSource) CommandEnabled(context.Context, string) (bool, error) {
	return true, nil
}

// controlPlaneTools builds the Officer service over a local node and the tool
// Source over it.
func controlPlaneTools(t *testing.T) (*backend.Service, frameworkmcp.Source) {
	t.Helper()
	target, _ := newLocalNode(t)
	service := newOfficerService(t, target, nil, nil)
	return service, enabledControlPlaneSource{frameworkmcp.SourceFor(service)}
}

// callToolOK calls one tool and fails the test on a tool error, naming it.
func callToolOK(
	t *testing.T, src frameworkmcp.Source, name string, in map[string]any,
) json.RawMessage {
	t.Helper()
	res := callTool[map[string]any](t, src, name, in)
	if res.IsError {
		t.Fatalf("%s: %s", name, textContent(res.Content))
	}
	return res.Wire
}

// assertResultBody fails the test unless a tool's structured result is body
// as REST writes it.
func assertResultBody(t *testing.T, wire json.RawMessage, body map[string]any) {
	t.Helper()
	want, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	assertTradingWire(t, wire, string(want))
}

// requireGone fails the test unless a read of what reports not found.
func requireGone(t *testing.T, what string, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("read %s: %v, want not found", what, err)
	}
}

// storedAsset finds the asset under code among the assets service lists, and
// reports whether it is stored.
func storedAsset(
	t *testing.T, service *backend.Service, code string,
) (domain.Asset, bool) {
	t.Helper()
	assets, err := service.ListAssets(systemCtx())
	if err != nil {
		t.Fatalf("list assets: %v", err)
	}
	for _, asset := range assets {
		if asset.Code == code {
			return asset, true
		}
	}
	return domain.Asset{}, false
}

// depositUSD deposits amount of USD into account through the service, as the
// operator funds an account that an agent, which may only lower a balance,
// then adjusts.
func depositUSD(
	t *testing.T, service *backend.Service, account domain.AccountID, amount string,
) {
	t.Helper()
	if _, err := service.ApplyAdjustment(systemCtx(), account, "", domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeDeposit, Asset: "USD",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: amount},
	}, domain.MissingAccountReject); err != nil {
		t.Fatalf("deposit %s USD into %s: %v", amount, account, err)
	}
}

// TestAccountToolsOnControlPlane drives the account tools an agent may use
// through the control-plane Source into a local service, on an account the
// service created: each result is the account the service reads back, the
// update moves the account from its old code to its new one, and the delete
// removes it.
func TestAccountToolsOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	// accountBody reads the account under code, requires the title sent, and
	// returns the body REST answers for it.
	accountBody := func(code domain.AccountID, title string) map[string]any {
		t.Helper()
		account, _, err := service.GetAccountState(ctx, code)
		if err != nil {
			t.Fatalf("read account %s: %v", code, err)
		}
		if account.Title != title {
			t.Fatalf("account %s title = %q, want %q", code, account.Title, title)
		}
		block := domain.ResolveAccountBlock(account, domain.AccountGroup{})
		return map[string]any{"account": httpx.ToAccountDTO(account, block)}
	}

	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1", Title: "Desk one"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}

	wire := callToolOK(t, src, "update_account",
		map[string]any{"account": "desk-1", "code": "desk-2", "title": "Desk two"})
	_, _, err := service.GetAccountState(ctx, "desk-1")
	requireGone(t, "account desk-1", err)
	assertResultBody(t, wire, accountBody("desk-2", "Desk two"))

	callToolOK(t, src, "delete_account", map[string]any{"account": "desk-2"})
	_, _, err = service.GetAccountState(ctx, "desk-2")
	requireGone(t, "account desk-2", err)
}

// TestSetAccountCurrencyOnControlPlane drives set_account_currency through the
// control-plane Source into a local service: the result is the account the
// service reads back, and a change of the currency of an account that holds a
// balance, or that a Spot Funds P&L bound applies to, is refused with the
// message REST answers for that constraint and changes nothing.
func TestSetAccountCurrencyOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	stored := func(code domain.AccountID) domain.Account {
		t.Helper()
		account, _, err := service.GetAccountState(ctx, code)
		if err != nil {
			t.Fatalf("read account %s: %v", code, err)
		}
		return account
	}
	// refusedAsREST changes the currency of code through the service and then
	// through the tool: both are refused with currency_change_blocked, the tool
	// with the message REST answers, and the stored currency stays kept. It
	// returns the service's refusal.
	refusedAsREST := func(code domain.AccountID, currency, kept string) error {
		t.Helper()
		restErr := service.SetAccountCurrency(ctx, code, currency)
		rest := httpx.DescribeErr(restErr)
		if rest.Code != "currency_change_blocked" {
			t.Fatalf("service currency change of %s = %v, want currency_change_blocked", code, restErr)
		}
		res := callTool[map[string]any](t, src, "set_account_currency",
			map[string]any{"account": string(code), "currency": currency})
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), "currency_change_blocked: "+rest.Message; got != want {
			t.Fatalf("%s refusal = %q, want REST's %q", code, got, want)
		}
		if got := stored(code).Currency; got != kept {
			t.Fatalf("account %s currency after the refusal = %q, want %s", code, got, kept)
		}
		return restErr
	}

	wire := callToolOK(t, src, "set_account_currency",
		map[string]any{"account": "desk-1", "currency": "USD"})
	account := stored("desk-1")
	if account.Currency != "USD" {
		t.Fatalf("account desk-1 currency = %q, want USD", account.Currency)
	}
	block := domain.ResolveAccountBlock(account, domain.AccountGroup{})
	assertResultBody(t, wire, map[string]any{"account": httpx.ToAccountDTO(account, block)})

	// A deposit leaves desk-1 holding a balance, so it is not economically
	// empty.
	if _, err := service.ApplyAdjustment(ctx, "desk-1", domain.ExternalID(""),
		sampleAdjustmentRequest(), domain.MissingAccountReject); err != nil {
		t.Fatalf("deposit into desk-1: %v", err)
	}
	if err := refusedAsREST("desk-1", "EUR", "USD"); errors.Is(err, domain.ErrCurrencyValuedLimit) {
		t.Fatalf("desk-1 refusal = %v, want the economically_empty constraint", err)
	}

	// desk-2 is economically empty, and an account-scoped Spot Funds P&L bound
	// applies to it.
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-2", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-2: %v", err)
	}
	if err := service.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccount, Account: "desk-2", Currency: "USD", LowerBound: "-1",
	}, domain.MissingAccountReject); err != nil {
		t.Fatalf("bound desk-2: %v", err)
	}
	if err := refusedAsREST("desk-2", "EUR", "USD"); !errors.Is(err, domain.ErrCurrencyValuedLimit) {
		t.Fatalf("desk-2 refusal = %v, want the currency_valued_limit constraint", err)
	}
}

// TestAssetToolsOnControlPlane drives the asset tools through the control-plane
// Source into a local service: each result is the asset the service lists, the
// update moves the asset from its old code to its new one, and the delete
// removes it.
func TestAssetToolsOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	// assetBody finds the asset under code, requires the title sent, and
	// returns the body REST answers for it.
	assetBody := func(code, title string) map[string]any {
		t.Helper()
		asset, ok := storedAsset(t, service, code)
		if !ok {
			t.Fatalf("asset %s is not stored", code)
		}
		if asset.Title != title {
			t.Fatalf("asset %s title = %q, want %q", code, asset.Title, title)
		}
		return map[string]any{"asset": httpx.ToAssetDTO(asset)}
	}

	wire := callToolOK(t, src, "create_asset", map[string]any{"code": "ETH", "title": "Ether"})
	assertResultBody(t, wire, assetBody("ETH", "Ether"))

	wire = callToolOK(t, src, "update_asset",
		map[string]any{"asset": "ETH", "code": "ETH2", "title": "Ether two"})
	if _, ok := storedAsset(t, service, "ETH"); ok {
		t.Fatal("asset ETH is still stored after the update")
	}
	assertResultBody(t, wire, assetBody("ETH2", "Ether two"))

	callToolOK(t, src, "delete_asset", map[string]any{"asset": "ETH2"})
	if _, ok := storedAsset(t, service, "ETH2"); ok {
		t.Fatal("asset ETH2 is still stored after the delete")
	}
}

// TestGroupToolsOnControlPlane drives the group tools through the control-plane
// Source into a local service: each result is the group REST answers, the
// service's read of it, the update moves the group from its old code to its
// new one, and the delete removes it.
func TestGroupToolsOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	// groupBody reads the group under code, requires the values sent, and
	// returns the body REST answers for it.
	groupBody := func(code string, want domain.AccountGroup) map[string]any {
		t.Helper()
		group, _, err := service.GetGroup(ctx, code)
		if err != nil {
			t.Fatalf("read group %s: %v", code, err)
		}
		if group.Title != want.Title || group.Currency != want.Currency || group.Notes != want.Notes {
			t.Fatalf("group %s = %+v, want title, currency and notes of %+v", code, group, want)
		}
		return map[string]any{"group": httpx.ToGroupDTO(group)}
	}
	sent := domain.AccountGroup{Title: "Desks", Currency: "USD", Notes: "floor"}

	wire := callToolOK(t, src, "create_group",
		map[string]any{"code": "desks", "title": "Desks", "currency": "USD", "notes": "floor"})
	assertResultBody(t, wire, groupBody("desks", sent))

	wire = callToolOK(t, src, "update_group",
		map[string]any{"group": "desks", "code": "traders", "title": "Traders"})
	_, _, err := service.GetGroup(ctx, "desks")
	requireGone(t, "group desks", err)
	sent.Title = "Traders"
	assertResultBody(t, wire, groupBody("traders", sent))

	callToolOK(t, src, "delete_group", map[string]any{"group": "traders"})
	_, _, err = service.GetGroup(ctx, "traders")
	requireGone(t, "group traders", err)
}

// TestDeleteGroupToolRefusesBlockedGroup drives delete_group through the
// control-plane Source into a local service: the core refuses the agent's
// delete of a blocked group, even with force, with loosening_refused and keeps
// the group blocked, while an unblocked group is deleted.
func TestDeleteGroupToolRefusesBlockedGroup(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	for _, code := range []string{"blocked", "open"} {
		if _, err := service.CreateGroup(ctx, domain.AccountGroup{Code: code}); err != nil {
			t.Fatalf("create group %s: %v", code, err)
		}
	}
	if err := service.SetGroupBlocked(ctx, "blocked", true, "risk"); err != nil {
		t.Fatalf("block group blocked: %v", err)
	}

	res := callTool[map[string]any](t, src, "delete_group",
		map[string]any{"group": "blocked", "force": true})
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, "loosening_refused: ") {
		t.Fatalf("delete_group of a blocked group = %q, want loosening_refused", text)
	}
	group, _, err := service.GetGroup(ctx, "blocked")
	if err != nil || !group.Blocked {
		t.Fatalf("group blocked after the refusal = %+v err=%v, want it stored and blocked", group, err)
	}

	callToolOK(t, src, "delete_group", map[string]any{"group": "open"})
	_, _, err = service.GetGroup(ctx, "open")
	requireGone(t, "group open", err)
}

// TestDeleteAccountToolRefusesBlockedAccount drives delete_account through the
// control-plane Source into a local service: the core refuses the agent's
// delete of a blocked account, even with force, with loosening_refused and
// keeps the account blocked, while an unprotected account is deleted.
func TestDeleteAccountToolRefusesBlockedAccount(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	for _, code := range []domain.AccountID{"blocked", "open"} {
		if _, err := service.CreateAccount(ctx, domain.Account{Code: code}); err != nil {
			t.Fatalf("create account %s: %v", code, err)
		}
	}
	if err := service.BlockAccount(ctx, "blocked", "risk", domain.MissingAccountReject); err != nil {
		t.Fatalf("block account blocked: %v", err)
	}

	res := callTool[map[string]any](t, src, "delete_account",
		map[string]any{"account": "blocked", "force": true})
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, "loosening_refused: ") {
		t.Fatalf("delete_account of a blocked account = %q, want loosening_refused", text)
	}
	account, _, err := service.GetAccountState(ctx, "blocked")
	if err != nil || !account.Blocked {
		t.Fatalf("account blocked after the refusal = %+v err=%v, want it stored and blocked", account, err)
	}

	callToolOK(t, src, "delete_account", map[string]any{"account": "open"})
	_, _, err = service.GetAccountState(ctx, "open")
	requireGone(t, "account open", err)
}

// TestBlockedAccountCodeNeverComesBackUnblocked drives, through the
// control-plane Source into a local service, the steps that would bring a
// blocked account's code back unblocked. The core refuses the agent's
// update_account renaming the blocked account away from its code with
// loosening_refused, so the code stays with the blocked account and no
// account takes the new code. Once the operator has renamed the account away
// through the service, which leaves the code free, the core refuses the
// agent's create_account under that code with loosening_refused too, so no
// account holds the code and the renamed account stays blocked.
func TestBlockedAccountCodeNeverComesBackUnblocked(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "blocked"}); err != nil {
		t.Fatalf("create account blocked: %v", err)
	}
	if err := service.BlockAccount(ctx, "blocked", "risk", domain.MissingAccountReject); err != nil {
		t.Fatalf("block account blocked: %v", err)
	}

	res := callTool[map[string]any](t, src, "update_account",
		map[string]any{"account": "blocked", "code": "renamed", "title": ""})
	requireLooseningRefused(t, "update_account renaming the blocked account", res)
	account, _, err := service.GetAccountState(ctx, "blocked")
	if err != nil || !account.Blocked {
		t.Fatalf("account blocked after the rename refusal = %+v err=%v, want it stored and blocked",
			account, err)
	}
	_, _, err = service.GetAccountState(ctx, "renamed")
	requireGone(t, "account renamed", err)

	if _, err := service.UpdateAccount(ctx, "blocked", domain.Account{Code: "renamed"}); err != nil {
		t.Fatalf("operator rename of account blocked: %v", err)
	}
	res = callTool[map[string]any](t, src, "create_account", map[string]any{"code": "blocked"})
	requireLooseningRefused(t, "create_account under the freed code", res)
	_, _, err = service.GetAccountState(ctx, "blocked")
	requireGone(t, "account blocked", err)
	account, _, err = service.GetAccountState(ctx, "renamed")
	if err != nil || !account.Blocked {
		t.Fatalf("account renamed after the create refusal = %+v err=%v, want it stored and blocked",
			account, err)
	}
}

// TestDeletedAccountCodeNeverComesBack drives delete_account and then
// submit_order through the control-plane Source into a local service with a
// signing key: the agent deletes an unprotected account, and the core refuses
// its order naming the deleted code with missingAccount create with
// loosening_refused, so the code does not come back as a new account with
// reset P&L and balances.
func TestDeletedAccountCodeNeverComesBack(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, chain, _ := reportFillChain(t)
	src := enabledControlPlaneSource{chain}
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "open"}); err != nil {
		t.Fatalf("create account open: %v", err)
	}

	callToolOK(t, src, "delete_account", map[string]any{"account": "open"})
	res := callSubmitOrder(t, src, submitOrderInput{
		Account: "open", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
		AmountKind: "quantity", AmountValue: "1", Price: "150", Mode: "hold",
		MissingAccount: "create",
	})
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, "loosening_refused: ") {
		t.Fatalf("submit_order naming the deleted code = %q, want loosening_refused", text)
	}
	_, _, err := service.GetAccountState(ctx, "open")
	requireGone(t, "account open", err)
}

// TestUpdateAssetToolRefusesCodeChangeOfBarrierAsset drives update_asset
// through the control-plane Source into a local service: the core refuses the
// agent's change of the code of an asset an order-size barrier references with
// loosening_refused and keeps the asset under its code, while a change of its
// title alone passes.
func TestUpdateAssetToolRefusesCodeChangeOfBarrierAsset(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAsset(ctx, domain.Asset{Code: "ETH"}); err != nil {
		t.Fatalf("create asset ETH: %v", err)
	}
	if err := service.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeUnderlyingAsset, Asset: "ETH", MaxQuantity: "1",
	}, domain.MissingAccountReject); err != nil {
		t.Fatalf("cap ETH: %v", err)
	}

	res := callTool[map[string]any](t, src, "update_asset",
		map[string]any{"asset": "ETH", "code": "ETH2"})
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, "loosening_refused: ") {
		t.Fatalf("update_asset of a capped asset = %q, want loosening_refused", text)
	}
	if _, ok := storedAsset(t, service, "ETH"); !ok {
		t.Fatal("asset ETH is gone after the refusal")
	}
	if _, ok := storedAsset(t, service, "ETH2"); ok {
		t.Fatal("asset ETH2 is stored after the refusal")
	}

	callToolOK(t, src, "update_asset",
		map[string]any{"asset": "ETH", "code": "ETH", "title": "Ether"})
	if asset, ok := storedAsset(t, service, "ETH"); !ok || asset.Title != "Ether" {
		t.Fatalf("asset ETH after the title change = %q stored=%v, want Ether", asset.Title, ok)
	}
}

// TestSetMarketDataInstrumentToolRefusesEnable drives set_market_data_instrument
// through the control-plane Source into a local service: the core lets the
// agent disable an instrument and refuses its enable with loosening_refused,
// so the instrument stays disabled.
func TestSetMarketDataInstrumentToolRefusesEnable(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	instance, err := service.CreateMarketDataInstance(ctx, domain.MarketDataInstance{
		Provider: domain.MarketDataProviderBinance, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create market-data instance: %v", err)
	}
	if err := service.UpsertMarketDataInstrument(ctx, domain.MarketDataInstrument{
		Instance: instance.ExternalID, ExternalSymbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", Enabled: true,
	}); err != nil {
		t.Fatalf("upsert market-data instrument: %v", err)
	}
	// storedEnabled reads whether the service lists BTCUSDT enabled.
	storedEnabled := func() bool {
		t.Helper()
		status, err := service.ListMarketData(ctx)
		if err != nil {
			t.Fatalf("list market data: %v", err)
		}
		for _, listed := range status.Instances {
			for _, instrument := range listed.Instruments {
				if instrument.Instrument.ExternalSymbol == "BTCUSDT" {
					return instrument.Instrument.Enabled
				}
			}
		}
		t.Fatal("instrument BTCUSDT is not listed")
		return false
	}
	toggle := func(enabled bool) map[string]any {
		return map[string]any{
			"id": instance.ExternalID.String(), "externalSymbol": "BTCUSDT",
			"enabled": enabled,
		}
	}

	callToolOK(t, src, "set_market_data_instrument", toggle(false))
	if storedEnabled() {
		t.Fatal("instrument BTCUSDT is enabled after the agent's disable")
	}

	res := callTool[map[string]any](t, src, "set_market_data_instrument", toggle(true))
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, "loosening_refused: ") {
		t.Fatalf("set_market_data_instrument enable = %q, want loosening_refused", text)
	}
	if storedEnabled() {
		t.Fatal("instrument BTCUSDT is enabled after the refusal")
	}
}

// appliedAsListed calls apply_adjustment on account with adjustment, requires
// its result to be the body REST answers for the record the service lists
// last for the account, and returns that record with the tool's text.
func appliedAsListed(
	t *testing.T, service *backend.Service, src frameworkmcp.Source,
	account domain.AccountID, adjustment map[string]any,
) (httpx.AdjustmentDTO, string) {
	t.Helper()
	res := callTool[map[string]any](t, src, "apply_adjustment", map[string]any{
		"account": string(account), "missingAccount": "reject", "adjustment": adjustment,
	})
	if res.IsError {
		t.Fatalf("apply_adjustment: %s", textContent(res.Content))
	}
	listed, err := service.ListAdjustments(systemCtx(), account, "", 1)
	if err != nil {
		t.Fatalf("list adjustments of %s: %v", account, err)
	}
	if len(listed) != 1 {
		t.Fatalf("adjustments of %s = %+v, want the one applied", account, listed)
	}
	record := httpx.ToAdjustmentDTO(listed[0])
	assertResultBody(t, res.Wire, map[string]any{"adjustment": record})
	return record, textContent(res.Content)
}

// outcomeJSON renders an adjustment outcome for a failure message.
func outcomeJSON(t *testing.T, outcome httpx.AdjustmentOutcomeDTO) string {
	t.Helper()
	raw, err := json.Marshal(outcome)
	if err != nil {
		t.Fatalf("marshal outcome: %v", err)
	}
	return string(raw)
}

// TestApplyAdjustmentArithmeticOnControlPlane drives apply_adjustment through
// the control-plane Source into a local service, on the balance of the
// account's currency USD the service funded, as an agent only lowers that one:
// the engine adds a delta and sets an absolute amount in exact decimal
// arithmetic, digits float64 cannot carry included, each result is the record
// the service lists, REST's answer, and the service reads the same balance
// back.
func TestApplyAdjustmentArithmeticOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "desk-1", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	depositUSD(t, service, "desk-1", "0.7")
	for _, step := range []struct {
		typ, mode, value, delta, result string
	}{
		{"withdrawal", "delta", "-0.1", "-0.1", "0.6"},
		// float64 subtracts these to 0.39999999999999997.
		{"withdrawal", "delta", "-0.2", "-0.2", "0.4"},
		// float64 cannot tell this result from 0.4.
		{"withdrawal", "delta", "-0.0000000000000000001", "-0.0000000000000000001", "0.3999999999999999999"},
		// An absolute amount sets the result; the delta is the change it made.
		{"correction", "absolute", "0.25", "-0.1499999999999999999", "0.25"},
	} {
		record, text := appliedAsListed(t, service, src, "desk-1", map[string]any{
			"type": step.typ, "asset": "USD",
			"balance": map[string]any{"mode": step.mode, "value": step.value},
		})
		if want := "adjustment " + record.ID + " accepted"; text != want {
			t.Fatalf("%s %s: text %q, want %q", step.mode, step.value, text, want)
		}
		accepted := record.Outcome.Accepted
		if accepted == nil || accepted.BalanceDelta != step.delta || accepted.BalanceResult != step.result {
			t.Fatalf("%s %s: outcome %s, want balance delta %s and result %s",
				step.mode, step.value, outcomeJSON(t, record.Outcome), step.delta, step.result)
		}
		if got := deskBalance(t, service, "USD").Available; got != step.result {
			t.Fatalf("%s %s: service reads balance %s, want %s", step.mode, step.value, got, step.result)
		}
	}
}

// TestApplyAdjustmentBoundsOnControlPlane drives apply_adjustment through the
// control-plane Source into a local service, on the balance of the account's
// currency USD the service funded, as an agent only lowers that one: a bound
// constrains the balance an adjustment results in, not its change. A small
// change whose result leaves the bounds is recorded as rejected with the
// engine's structured reason and changes no balance, whichever bound of
// balance, held or incoming it leaves; a large change whose result lands on an
// inclusive bound is accepted, and a bound on a balance the adjustment leaves
// alone is not checked. Each result is the record the service lists, REST's
// answer.
func TestApplyAdjustmentBoundsOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "desk-1", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	depositUSD(t, service, "desk-1", "100")

	// The change of -1 lies within the bounds; the result of 99 does not.
	record, text := appliedAsListed(t, service, src, "desk-1", map[string]any{
		"type": "withdrawal", "asset": "USD",
		"balance":       map[string]any{"mode": "delta", "value": "-1"},
		"balanceBounds": map[string]any{"lower": "-1", "upper": "50"},
	})
	rejected := record.Outcome.Rejected
	if record.Status != string(domain.AdjustmentStatusRejected) || rejected == nil ||
		rejected.Code != domain.RejectCodeAccountAdjustmentBoundsExceeded || rejected.Reason == "" {
		t.Fatalf("result out of bounds: status %s, outcome %s, want it rejected for its bounds",
			record.Status, outcomeJSON(t, record.Outcome))
	}
	if want := "adjustment " + record.ID + " rejected: " + rejected.Reason; text != want {
		t.Fatalf("rejection text %q, want %q", text, want)
	}
	if got := deskBalance(t, service, "USD").Available; got != "100" {
		t.Fatalf("balance after the rejection = %s, want 100 unchanged", got)
	}

	// Each bound of each field reaches the engine: a result past a lower
	// bound, and one past a held or an incoming bound, is rejected as well.
	for _, step := range []struct {
		name       string
		adjustment map[string]any
	}{
		{"balance lower", map[string]any{
			"type": "withdrawal", "asset": "USD",
			"balance":       map[string]any{"mode": "delta", "value": "-1"},
			"balanceBounds": map[string]any{"lower": "100"},
		}},
		{"held upper", map[string]any{
			"type": "correction", "asset": "USD",
			"held":       map[string]any{"mode": "delta", "value": "-1"},
			"heldBounds": map[string]any{"upper": "-2"},
		}},
		{"incoming lower", map[string]any{
			"type": "correction", "asset": "USD",
			"incoming":       map[string]any{"mode": "delta", "value": "-1"},
			"incomingBounds": map[string]any{"lower": "0"},
		}},
	} {
		record, _ := appliedAsListed(t, service, src, "desk-1", step.adjustment)
		if rejected := record.Outcome.Rejected; record.Status != string(domain.AdjustmentStatusRejected) ||
			rejected == nil || rejected.Code != domain.RejectCodeAccountAdjustmentBoundsExceeded {
			t.Fatalf("%s bound left: status %s, outcome %s, want it rejected for its bounds",
				step.name, record.Status, outcomeJSON(t, record.Outcome))
		}
		if got := deskBalance(t, service, "USD"); got.Available != "100" || got.Held != "0" || got.Incoming != "0" {
			t.Fatalf("balance after the %s rejection = %+v, want 100, 0 and 0 unchanged", step.name, got)
		}
	}

	// The change of -90 lies below the bounds; the result of 10 is their
	// inclusive lower bound.
	record, _ = appliedAsListed(t, service, src, "desk-1", map[string]any{
		"type": "withdrawal", "asset": "USD",
		"balance":       map[string]any{"mode": "delta", "value": "-90"},
		"balanceBounds": map[string]any{"lower": "10", "upper": "100"},
	})
	if accepted := record.Outcome.Accepted; accepted == nil || accepted.BalanceResult != "10" {
		t.Fatalf("result on its bound: outcome %s, want it accepted with balance 10",
			outcomeJSON(t, record.Outcome))
	}
	if got := deskBalance(t, service, "USD").Available; got != "10" {
		t.Fatalf("balance after the withdrawal = %s, want 10", got)
	}

	// The balance of 10 lies above its bound, which is not checked, as the
	// adjustment changes only held.
	record, _ = appliedAsListed(t, service, src, "desk-1", map[string]any{
		"type": "correction", "asset": "USD",
		"held":          map[string]any{"mode": "delta", "value": "-1"},
		"balanceBounds": map[string]any{"upper": "5"},
	})
	if accepted := record.Outcome.Accepted; accepted == nil || accepted.HeldResult != "-1" {
		t.Fatalf("held change: outcome %s, want it accepted with held -1",
			outcomeJSON(t, record.Outcome))
	}
	if got := deskBalance(t, service, "USD"); got.Available != "10" || got.Held != "-1" {
		t.Fatalf("balance after the held change = %+v, want available 10 and held -1", got)
	}
}

// TestApplyAdjustmentRefusalsOnControlPlane drives apply_adjustment through the
// control-plane Source into a local service, on the balance of the account's
// currency USD the service funded, as an agent only lowers that one. The
// backend refuses a named account's absent or unknown missing-account policy
// with the answer REST gives when it parses the policy itself, a duplicate id
// and an account the policy refuses as REST does, and an adjustment the engine
// reports as changing nothing is a success without an adjustment. None of them
// records one.
func TestApplyAdjustmentRefusalsOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	depositUSD(t, service, "desk-1", "100")
	withdrawal := map[string]any{
		"id": "adj-1", "type": "withdrawal", "asset": "USD",
		"balance": map[string]any{"mode": "delta", "value": "-1"},
	}
	if record, _ := appliedAsListed(t, service, src, "desk-1", withdrawal); record.ID != "adj-1" {
		t.Fatalf("record id = %q, want the supplied adj-1", record.ID)
	}
	request := domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeWithdrawal, Asset: "USD",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "-1"},
	}
	// refusedAsREST requires the tool to refuse in with REST's answer to
	// restErr.
	refusedAsREST := func(in map[string]any, restErr error) {
		t.Helper()
		rest := httpx.DescribeErr(restErr)
		res := callTool[map[string]any](t, src, "apply_adjustment", in)
		requireToolError(t, res.IsError)
		if got, want := textContent(res.Content), rest.Code+": "+rest.Message; got != want {
			t.Fatalf("refusal = %q, want REST's %q", got, want)
		}
	}

	for _, policy := range []string{"", "bogus"} {
		_, restErr := domain.ParseMissingAccountPolicy(policy)
		in := map[string]any{"account": "desk-1", "adjustment": withdrawal}
		if policy != "" {
			in["missingAccount"] = policy
		}
		refusedAsREST(in, restErr)
	}
	_, restErr := service.ApplyAdjustment(ctx, "desk-1", "adj-1", request, domain.MissingAccountReject)
	if !errors.Is(restErr, domain.ErrAlreadyExists) {
		t.Fatalf("service duplicate id = %v, want already exists", restErr)
	}
	refusedAsREST(map[string]any{
		"account": "desk-1", "missingAccount": "reject", "adjustment": withdrawal,
	}, restErr)
	_, restErr = service.ApplyAdjustment(ctx, "ghost", "", request, domain.MissingAccountReject)
	if !errors.Is(restErr, domain.ErrAccountMissing) {
		t.Fatalf("service missing account = %v, want account missing", restErr)
	}
	refusedAsREST(map[string]any{
		"account": "ghost", "missingAccount": "reject",
		"adjustment": map[string]any{"type": "withdrawal", "asset": "USD",
			"balance": map[string]any{"mode": "delta", "value": "-1"}},
	}, restErr)

	// An adjustment naming no field leaves the engine nothing to change.
	res := callTool[map[string]any](t, src, "apply_adjustment", map[string]any{
		"account": "desk-1", "missingAccount": "reject",
		"adjustment": map[string]any{"type": "deposit", "asset": "USD"},
	})
	requireNotToolError(t, res.IsError)
	if got, want := textContent(res.Content), "the engine reported no change; no adjustment was recorded"; got != want {
		t.Fatalf("no-change text = %q, want %q", got, want)
	}
	assertTradingWire(t, res.Wire, `{}`)

	listed, err := service.ListAdjustments(ctx, "desk-1", "", 10)
	if err != nil {
		t.Fatalf("list adjustments of desk-1: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("adjustments of desk-1 = %+v, want only the deposit and the first withdrawal", listed)
	}
}

// TestApplyAdjustmentMissingAccountWithOneAccount drives apply_adjustment
// through the control-plane Source of a service in single-account mode, on
// the balance of the account's currency USD the service funded, as an agent
// only lowers that one, with a missingAccount that is no policy: an adjustment
// that names no account ignores it, as REST ignores the query parameter of a
// request that names none, and is applied to the one account; one that names
// the account is refused with validation and records nothing.
func TestApplyAdjustmentMissingAccountWithOneAccount(t *testing.T) {
	t.Parallel()
	service, src := oneAccountControlPlaneTools(t)
	if err := service.SetAccountCurrency(
		systemCtx(), oneAccountFeatures.Account, "USD",
	); err != nil {
		t.Fatalf("set the currency of %s: %v", oneAccountFeatures.Account, err)
	}
	depositUSD(t, service, oneAccountFeatures.Account, "100")
	withdrawal := map[string]any{
		"type": "withdrawal", "asset": "USD",
		"balance": map[string]any{"mode": "delta", "value": "-1"},
	}
	callToolOK(t, src, "apply_adjustment", map[string]any{
		"missingAccount": "bogus", "adjustment": withdrawal,
	})
	res := callTool[map[string]any](t, src, "apply_adjustment", map[string]any{
		"account": string(oneAccountFeatures.Account), "missingAccount": "bogus",
		"adjustment": withdrawal,
	})
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, "validation: ") {
		t.Fatalf("apply_adjustment naming the account = %q, want validation", text)
	}
	listed, err := service.ListAdjustments(systemCtx(), oneAccountFeatures.Account, "", 10)
	if err != nil {
		t.Fatalf("list adjustments of %s: %v", oneAccountFeatures.Account, err)
	}
	if len(listed) != 2 {
		t.Fatalf("adjustments of %s = %+v, want only the deposit and the first withdrawal",
			oneAccountFeatures.Account, listed)
	}
}

// TestApplyAdjustmentOnlyLowersOnControlPlane drives apply_adjustment through
// the control-plane Source into a local service, on the balance of the
// account's currency USD the service funded: the core refuses the agent's
// adjustment that raises available funds, by a delta or to an absolute amount,
// with loosening_refused and records nothing, so the balance stays, while the
// agent's withdrawal lowers it.
func TestApplyAdjustmentOnlyLowersOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	depositUSD(t, service, "desk-1", "100")

	for _, raising := range []map[string]any{
		{"type": "deposit", "asset": "USD",
			"balance": map[string]any{"mode": "delta", "value": "1"}},
		{"type": "correction", "asset": "USD",
			"balance": map[string]any{"mode": "absolute", "value": "100.5"}},
	} {
		res := callTool[map[string]any](t, src, "apply_adjustment", map[string]any{
			"account": "desk-1", "missingAccount": "reject", "adjustment": raising,
		})
		requireLooseningRefused(t, fmt.Sprintf("apply_adjustment raising available funds with %v", raising), res)
		if got := deskBalance(t, service, "USD").Available; got != "100" {
			t.Fatalf("balance after the refusal = %s, want 100 unchanged", got)
		}
	}
	listed, err := service.ListAdjustments(ctx, "desk-1", "", 10)
	if err != nil {
		t.Fatalf("list adjustments of desk-1: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("adjustments of desk-1 = %+v, want only the deposit", listed)
	}

	record, _ := appliedAsListed(t, service, src, "desk-1", map[string]any{
		"type": "withdrawal", "asset": "USD",
		"balance": map[string]any{"mode": "delta", "value": "-1"},
	})
	if accepted := record.Outcome.Accepted; accepted == nil || accepted.BalanceResult != "99" {
		t.Fatalf("withdrawal: outcome %s, want it accepted with balance 99",
			outcomeJSON(t, record.Outcome))
	}
	if got := deskBalance(t, service, "USD").Available; got != "99" {
		t.Fatalf("balance after the withdrawal = %s, want 99", got)
	}
}

// TestApplyAdjustmentRefusesPositionAndBooksOnControlPlane drives
// apply_adjustment through the control-plane Source into a local service, on
// desk-1 in USD, which the service funded and gave a position in AAPL: the
// core refuses the agent's withdrawal from the position, as AAPL is not the
// account's currency, and its averageEntryPrice and its realizedPnl for USD
// other than the stored ones, each with loosening_refused naming what an agent
// may change, and records nothing, so both balances stay.
func TestApplyAdjustmentRefusesPositionAndBooksOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	depositUSD(t, service, "desk-1", "100")
	if _, err := service.ApplyAdjustment(ctx, "desk-1", "", domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeCorrection, Asset: "AAPL",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "10"},
	}, domain.MissingAccountReject); err != nil {
		t.Fatalf("book 10 AAPL into desk-1: %v", err)
	}
	usd, aapl := deskBalance(t, service, "USD"), deskBalance(t, service, "AAPL")

	for _, step := range []struct {
		name, refusal string
		adjustment    map[string]any
	}{
		{
			"position",
			`apply adjustment for account "desk-1": an agent may only lower the ` +
				"balance, held or incoming of the account's currency USD; asset AAPL " +
				"is not it: " + domain.ErrLoosening.Error(),
			map[string]any{"type": "withdrawal", "asset": "AAPL",
				"balance": map[string]any{"mode": "delta", "value": "-1"}},
		},
		{
			"average entry price",
			"an agent may not change the average entry price or the realized P&L; " +
				"average entry price 7 ",
			map[string]any{"type": "correction", "asset": "USD", "averageEntryPrice": "7"},
		},
		{
			"realized P&L",
			"an agent may not change the average entry price or the realized P&L; " +
				"realized P&L 7 ",
			map[string]any{"type": "correction", "asset": "USD", "realizedPnl": "7"},
		},
	} {
		res := callTool[map[string]any](t, src, "apply_adjustment", map[string]any{
			"account": "desk-1", "missingAccount": "reject", "adjustment": step.adjustment,
		})
		requireLooseningRefused(t, "apply_adjustment of the "+step.name, res)
		if text := textContent(res.Content); !strings.Contains(text, step.refusal) {
			t.Fatalf("apply_adjustment of the %s = %q, want loosening_refused with %q",
				step.name, text, step.refusal)
		}
	}
	if got := deskBalance(t, service, "USD"); got != usd {
		t.Fatalf("USD balance after the refusals = %+v, want %+v unchanged", got, usd)
	}
	if got := deskBalance(t, service, "AAPL"); got != aapl {
		t.Fatalf("AAPL balance after the refusals = %+v, want %+v unchanged", got, aapl)
	}
	listed, err := service.ListAdjustments(ctx, "desk-1", "", 10)
	if err != nil {
		t.Fatalf("list adjustments of desk-1: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("adjustments of desk-1 = %+v, want only the deposit and the AAPL booking", listed)
	}
}

// TestApplyAdjustmentRefusesTradingRuntimeIDOnControlPlane drives
// apply_adjustment through the control-plane Source into a local service, on
// desk-1 in USD, which the service funded: the core refuses the agent's
// withdrawal under the id the trading runtime books a venue fee under with
// loosening_refused, saying the namespace is reserved for the runtime, and
// records nothing, so the balance stays and the id stays free for the fee.
func TestApplyAdjustmentRefusesTradingRuntimeIDOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1", Currency: "USD"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	depositUSD(t, service, "desk-1", "100")

	res := callTool[map[string]any](t, src, "apply_adjustment", map[string]any{
		"account": "desk-1", "missingAccount": "reject",
		"adjustment": map[string]any{
			"id": "trading:venue-1:fee:f-1", "type": "withdrawal", "asset": "USD",
			"balance": map[string]any{"mode": "delta", "value": "-1"},
		},
	})
	requireToolError(t, res.IsError)
	refusal := `apply adjustment for account "desk-1": adjustment id ` +
		`"trading:venue-1:fee:f-1": an id starting with "trading:" is reserved ` +
		"for the trading runtime: " + domain.ErrLoosening.Error()
	if text := textContent(res.Content); !strings.HasPrefix(text, "loosening_refused: ") ||
		!strings.Contains(text, refusal) {
		t.Fatalf("apply_adjustment under a venue fee's id = %q, want loosening_refused with %q",
			text, refusal)
	}
	if got := deskBalance(t, service, "USD").Available; got != "100" {
		t.Fatalf("balance after the refusal = %s, want 100 unchanged", got)
	}
	listed, err := service.ListAdjustments(ctx, "desk-1", "", 10)
	if err != nil {
		t.Fatalf("list adjustments of desk-1: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("adjustments of desk-1 = %+v, want only the deposit", listed)
	}
}
