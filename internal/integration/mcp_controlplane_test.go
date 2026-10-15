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

// TestAccountToolsOnControlPlane drives the account tools through the
// control-plane Source into a local service: each result is the account the
// service reads back, the update moves the account from its old code to its
// new one, and the delete removes it.
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

	wire := callToolOK(t, src, "create_account", map[string]any{"code": "desk-1", "title": "Desk one"})
	assertResultBody(t, wire, accountBody("desk-1", "Desk one"))

	wire = callToolOK(t, src, "update_account",
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
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	stored := func(code string) (domain.Asset, bool) {
		t.Helper()
		assets, err := service.ListAssets(ctx)
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
	// assetBody finds the asset under code, requires the title sent, and
	// returns the body REST answers for it.
	assetBody := func(code, title string) map[string]any {
		t.Helper()
		asset, ok := stored(code)
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
	if _, ok := stored("ETH"); ok {
		t.Fatal("asset ETH is still stored after the update")
	}
	assertResultBody(t, wire, assetBody("ETH2", "Ether two"))

	callToolOK(t, src, "delete_asset", map[string]any{"asset": "ETH2"})
	if _, ok := stored("ETH2"); ok {
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
// the control-plane Source into a local service: the engine adds a delta and
// sets an absolute amount in exact decimal arithmetic, digits float64 cannot
// carry included, each result is the record the service lists, REST's answer,
// and the service reads the same balance back.
func TestApplyAdjustmentArithmeticOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "desk-1"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	for _, step := range []struct {
		typ, mode, value, delta, result string
	}{
		{"deposit", "delta", "0.1", "0.1", "0.1"},
		// float64 sums these to 0.30000000000000004.
		{"deposit", "delta", "0.2", "0.2", "0.3"},
		// float64 cannot tell this result from 0.3.
		{"deposit", "delta", "0.0000000000000000001", "0.0000000000000000001", "0.3000000000000000001"},
		// An absolute amount sets the result; the delta is the change it made.
		{"correction", "absolute", "7.25", "6.9499999999999999999", "7.25"},
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
// control-plane Source into a local service: a bound constrains the balance an
// adjustment results in, not its change. A small change whose result leaves
// the bounds is recorded as rejected with the engine's structured reason and
// changes no balance, whichever bound of balance, held or incoming it leaves;
// a large change whose result lands on an inclusive bound
// is accepted, and a bound on a balance the adjustment leaves alone is not
// checked. Each result is the record the service lists, REST's answer.
func TestApplyAdjustmentBoundsOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "desk-1"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	appliedAsListed(t, service, src, "desk-1", map[string]any{
		"type": "deposit", "asset": "USD",
		"balance": map[string]any{"mode": "absolute", "value": "100"},
	})

	// The change of 1 lies within the bounds; the result of 101 does not.
	record, text := appliedAsListed(t, service, src, "desk-1", map[string]any{
		"type": "deposit", "asset": "USD",
		"balance":       map[string]any{"mode": "delta", "value": "1"},
		"balanceBounds": map[string]any{"lower": "0", "upper": "100"},
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
			"held":       map[string]any{"mode": "delta", "value": "1"},
			"heldBounds": map[string]any{"upper": "0"},
		}},
		{"incoming lower", map[string]any{
			"type": "correction", "asset": "USD",
			"incoming":       map[string]any{"mode": "delta", "value": "1"},
			"incomingBounds": map[string]any{"lower": "2"},
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
		"held":          map[string]any{"mode": "delta", "value": "1"},
		"balanceBounds": map[string]any{"upper": "5"},
	})
	if accepted := record.Outcome.Accepted; accepted == nil || accepted.HeldResult != "1" {
		t.Fatalf("held change: outcome %s, want it accepted with held 1",
			outcomeJSON(t, record.Outcome))
	}
	if got := deskBalance(t, service, "USD"); got.Available != "10" || got.Held != "1" {
		t.Fatalf("balance after the held change = %+v, want available 10 and held 1", got)
	}
}

// TestApplyAdjustmentRefusalsOnControlPlane drives apply_adjustment through the
// control-plane Source into a local service. The backend refuses a named
// account's absent or unknown missing-account policy with the answer REST
// gives when it parses the policy itself, a duplicate id and an account the
// policy refuses as REST does, and an adjustment the engine reports as
// changing nothing is a success without an adjustment. None of them records
// one.
func TestApplyAdjustmentRefusalsOnControlPlane(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk-1"}); err != nil {
		t.Fatalf("create account desk-1: %v", err)
	}
	deposit := map[string]any{
		"id": "adj-1", "type": "deposit", "asset": "USD",
		"balance": map[string]any{"mode": "delta", "value": "1"},
	}
	if record, _ := appliedAsListed(t, service, src, "desk-1", deposit); record.ID != "adj-1" {
		t.Fatalf("record id = %q, want the supplied adj-1", record.ID)
	}
	request := domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeDeposit, Asset: "USD",
		Balance: &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "1"},
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
		in := map[string]any{"account": "desk-1", "adjustment": deposit}
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
		"account": "desk-1", "missingAccount": "reject", "adjustment": deposit,
	}, restErr)
	_, restErr = service.ApplyAdjustment(ctx, "ghost", "", request, domain.MissingAccountReject)
	if !errors.Is(restErr, domain.ErrAccountMissing) {
		t.Fatalf("service missing account = %v, want account missing", restErr)
	}
	refusedAsREST(map[string]any{
		"account": "ghost", "missingAccount": "reject",
		"adjustment": map[string]any{"type": "deposit", "asset": "USD",
			"balance": map[string]any{"mode": "delta", "value": "1"}},
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
	if len(listed) != 1 {
		t.Fatalf("adjustments of desk-1 = %+v, want only the first deposit", listed)
	}
}
