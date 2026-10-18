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
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/businesscsv"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

const (
	// scopeClientAccount is the account a client names and
	// scopeResolvedAccount the one the recording scope resolves it to, so the
	// value reaching the node shows which of the two the backend passed on.
	scopeClientAccount   domain.AccountID = "client-acc"
	scopeResolvedAccount domain.AccountID = "scoped-acc"
	// scopeOwnerAccount owns scopeOwnedOrder, the order the by-id operations
	// address.
	scopeOwnerAccount domain.AccountID  = "owner-acc"
	scopeOwnedOrder   domain.ExternalID = "owned-order"
	scopeClientGroup                    = "client-desk"
	scopeConnection   domain.ExternalID = "connection-1"
	// scopeClientPolicy is the missing-account policy a client sends and
	// scopeResolvedPolicy the one the recording scope answers with.
	scopeClientPolicy   = domain.MissingAccountCreate
	scopeResolvedPolicy = domain.MissingAccountReject
)

var errScopeRefused = errors.New("account scope refused the request")

// scopeCall is one question a recordingScope was asked: the method, the
// caller, and the raw value the backend passed.
type scopeCall struct {
	method string
	caller domain.Caller
	raw    string
}

// recordingScope is a restrictive account scope. It records every question
// with its caller and raw value and answers each with the configured account,
// policy and error; a list restriction keeps the rows with no account when
// unowned is set, and Visible also refuses every hidden owner.
type recordingScope struct {
	account domain.AccountID
	policy  domain.MissingAccountPolicy
	unowned bool
	hidden  []domain.AccountID
	err     error
	calls   []scopeCall
}

var _ backend.AccountScope = (*recordingScope)(nil)

func (s *recordingScope) record(method string, caller domain.Caller, raw string) {
	s.calls = append(s.calls, scopeCall{method: method, caller: caller, raw: raw})
}

func (s *recordingScope) Features() backend.AccountFeatures {
	return backend.AccountFeatures{Account: s.account}
}

func (s *recordingScope) Account(
	caller domain.Caller, requested domain.AccountID,
) (domain.AccountID, error) {
	s.record("Account", caller, string(requested))
	if s.err != nil {
		return "", s.err
	}
	return s.account, nil
}

func (s *recordingScope) MissingAccount(
	caller domain.Caller, requested domain.MissingAccountPolicy,
) (domain.MissingAccountPolicy, error) {
	s.record("MissingAccount", caller, string(requested))
	if s.err != nil {
		return "", s.err
	}
	return s.policy, nil
}

func (s *recordingScope) List(
	caller domain.Caller, requested domain.AccountID,
) (domain.AccountRestriction, error) {
	s.record("List", caller, string(requested))
	if s.err != nil {
		return domain.AccountRestriction{}, s.err
	}
	return domain.AccountRestriction{Account: s.account, IncludeUnowned: s.unowned}, nil
}

func (s *recordingScope) Visible(caller domain.Caller, owner domain.AccountID) error {
	s.record("Visible", caller, string(owner))
	if slices.Contains(s.hidden, owner) {
		return errScopeRefused
	}
	return s.err
}

func (s *recordingScope) Group(caller domain.Caller, requested string) error {
	s.record("Group", caller, requested)
	return s.err
}

func (s *recordingScope) ManageAccounts(caller domain.Caller) error {
	s.record("ManageAccounts", caller, "")
	return s.err
}

// scopeTradingRuntime is a venue runtime with one paper provider that keeps
// unlinked fees and a venue that accepts every pre-check and send.
type scopeTradingRuntime struct{ backend.TradingRuntime }

func (scopeTradingRuntime) Providers() []trading.Provider {
	return []trading.Provider{{
		Type: "venue", Title: "Venue", VenueAccounts: true, UnlinkedFees: true,
	}}
}

func (scopeTradingRuntime) ConnectionStatuses() map[domain.ExternalID]trading.ConnectionStatus {
	return nil
}

func (scopeTradingRuntime) PreCheck(
	context.Context, domain.Order, domain.TradingDestination,
) error {
	return nil
}

func (scopeTradingRuntime) Send(
	context.Context, domain.ExternalID, domain.TradingDestination,
) (domain.VenueOrder, error) {
	return domain.VenueOrder{}, nil
}

// newScopeTestService builds the Officer service over a fake node holding one
// paper connection granted to scopeResolvedAccount, a fake signer and a venue
// runtime, with scope as its account scope. The signer is returned so a test
// can seed signed orders through a default-scope service over the same node.
func newScopeTestService(
	t *testing.T, scope backend.AccountScope,
) (*backend.Service, *fakeNode, *fakeSigner) {
	t.Helper()
	fn := &fakeNode{
		orders: make(map[domain.ExternalID]domain.Order),
		tradingConnections: []domain.TradingConnection{{
			ExternalID: scopeConnection, Provider: "venue", Label: "Paper",
			Mode: domain.TradingModePaper, VenueIdentity: "venue-1",
			Credentials: `{"apiKey":"key-value","apiSecret":"secret-value"}`,
			Enabled:     true,
		}},
		tradingAccess: []domain.TradingAccess{{
			Account: scopeResolvedAccount, Connection: scopeConnection,
		}},
	}
	signer := &fakeSigner{}
	svc := newOfficerService(
		t, fn, nil, signer,
		backend.WithAccountScope(scope),
		backend.WithTradingRuntime(scopeTradingRuntime{}),
	)
	return svc, fn, signer
}

// seedOwnedOrder submits scopeOwnedOrder, a signed hold order of
// scopeOwnerAccount, through a default-scope service over fn and signer.
func seedOwnedOrder(t *testing.T, fn *fakeNode, signer *fakeSigner) {
	t.Helper()
	order := sampleOrder()
	order.ExternalID = scopeOwnedOrder
	order.Account = scopeOwnerAccount
	seed := newOfficerService(t, fn, nil, signer)
	if _, err := seed.SubmitOrder(
		systemCtx(), order, backend.SubmitModeHold, domain.MissingAccountCreate,
	); err != nil {
		t.Fatalf("seed owned order: %v", err)
	}
}

// scopeRequest is the raw account and missing-account policy a client sends
// to a site; a site that takes neither ignores it.
type scopeRequest struct {
	account domain.AccountID
	policy  domain.MissingAccountPolicy
}

// scopeNamedRequest names the client's account and policy; scopeOmittedRequest
// omits both, leaving the scope to supply them.
var (
	scopeNamedRequest   = scopeRequest{account: scopeClientAccount, policy: scopeClientPolicy}
	scopeOmittedRequest = scopeRequest{}
)

// scopeSite is one backend operation that consults the account scope.
type scopeSite struct {
	name string
	// owned seeds scopeOwnedOrder before the operation runs.
	owned bool
	// omitClears marks a site where an omitted account clears the target
	// instead of being resolved, so the scope is not asked for one.
	omitClears bool
	// run performs the operation with the client's raw values.
	run func(ctx context.Context, svc *backend.Service, req scopeRequest) error
	// asked is the questions the scope gets for scopeNamedRequest, method and
	// raw value, in order.
	asked []scopeCall
	// reached is what the node receives when the scope allows the request.
	reached []string
}

// resolvesAccount reports whether the site resolves the account the client
// names, so an omitted one is supplied by the scope.
func (site scopeSite) resolvesAccount() bool {
	return !site.omitClears && len(site.asked) > 0 &&
		site.asked[0] == scopeCall{method: "Account", raw: string(scopeClientAccount)}
}

// takesPolicy reports whether the site sends the client's missing-account
// policy along with the account it resolves.
func (site scopeSite) takesPolicy() bool {
	return site.resolvesAccount() && slices.Contains(
		site.asked, scopeCall{method: "MissingAccount", raw: string(scopeClientPolicy)},
	)
}

// omittedAsked is asked for scopeOmittedRequest: every question the client's
// account or policy fed is asked with "" instead.
func (site scopeSite) omittedAsked() []scopeCall {
	asked := slices.Clone(site.asked)
	for i, call := range asked {
		if call == (scopeCall{method: "Account", raw: string(scopeClientAccount)}) ||
			call == (scopeCall{method: "MissingAccount", raw: string(scopeClientPolicy)}) {
			asked[i].raw = ""
		}
	}
	return asked
}

// prepare builds a fresh scope and service and seeds the owned order when the
// site addresses one, leaving both call logs empty.
func (site scopeSite) prepare(
	t *testing.T,
) (*recordingScope, *backend.Service, *fakeNode) {
	t.Helper()
	scope := &recordingScope{
		account: scopeResolvedAccount, policy: scopeResolvedPolicy,
	}
	svc, fn, signer := newScopeTestService(t, scope)
	if site.owned {
		seedOwnedOrder(t, fn, signer)
		fn.accountCalls = nil
	}
	return scope, svc, fn
}

func accountScopeSites() []scopeSite {
	account := func(raw domain.AccountID) scopeCall {
		return scopeCall{method: "Account", raw: string(raw)}
	}
	policy := func(raw domain.MissingAccountPolicy) scopeCall {
		return scopeCall{method: "MissingAccount", raw: string(raw)}
	}
	group := scopeCall{method: "Group", raw: scopeClientGroup}
	visible := scopeCall{method: "Visible", raw: string(scopeOwnerAccount)}
	manage := scopeCall{method: "ManageAccounts"}
	clientOrder := func(req scopeRequest) domain.Order {
		order := sampleOrder()
		order.Account = req.account
		return order
	}

	return []scopeSite{
		{
			name: "BlockAccount",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.BlockAccount(ctx, req.account, "risk", req.policy)
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`SetAccountBlocked "scoped-acc" "reject"`},
		},
		{
			name: "UnblockAccount",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.UnblockAccount(ctx, req.account, req.policy)
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`SetAccountBlocked "scoped-acc" "reject"`},
		},
		{
			name: "GetAccountState",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, _, err := svc.GetAccountState(ctx, req.account)
				return err
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`GetAccountState "scoped-acc"`},
		},
		{
			name: "SetAccountCurrency",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.SetAccountCurrency(ctx, req.account, "USD")
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`SetAccountCurrency "scoped-acc"`},
		},
		{
			name: "SetAccountNotes",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.SetAccountNotes(ctx, req.account, "watch")
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`SetAccountNotes "scoped-acc"`},
		},
		{
			name: "PutRateLimit",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.PutRateLimit(ctx, domain.LimitRate{
					Scope: domain.ScopeAccount, Account: req.account,
					Window: time.Second, MaxOrders: 10,
				}, req.policy)
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`PutRateLimit "scoped-acc" "reject"`},
		},
		{
			name: "PutOrderSizeLimit",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
					Scope:   domain.ScopeAccountSettlementAsset,
					Account: req.account, Asset: "USD", MaxNotional: "1000",
				}, req.policy)
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`PutOrderSizeLimit "scoped-acc" "reject"`},
		},
		{
			name: "PutSpotFundsPnlBoundsLimit/account",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
					Scope: domain.ScopeAccount, Account: req.account,
					Currency: "USD", LowerBound: "-1",
				}, req.policy)
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`PutSpotFundsPnlBoundsLimit "scoped-acc" "" "reject"`},
		},
		{
			name: "PutSpotFundsPnlBoundsLimit/account_group",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
					Scope: domain.ScopeAccountGroup, AccountGroup: scopeClientGroup,
					Currency: "USD", LowerBound: "-1",
				}, req.policy)
			},
			asked:   []scopeCall{group, policy(scopeClientPolicy)},
			reached: []string{`PutSpotFundsPnlBoundsLimit "" "client-desk" "reject"`},
		},
		{
			name: "DeleteLimit/account",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.DeleteLimit(ctx, node.LimitTarget{
					Policy: domain.PolicyRateLimit, Scope: domain.ScopeAccount,
					Account: req.account,
				})
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`DeleteLimit "scoped-acc" ""`},
		},
		{
			name: "DeleteLimit/account_group",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.DeleteLimit(ctx, node.LimitTarget{
					Policy: domain.PolicySpotFundsPnlBoundsKillSwitch,
					Scope:  domain.ScopeAccountGroup, AccountGroup: scopeClientGroup,
				})
			},
			asked:   []scopeCall{group},
			reached: []string{`DeleteLimit "" "client-desk"`},
		},
		{
			name: "ApplyAdjustment",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, err := svc.ApplyAdjustment(
					ctx, req.account, "", domain.AdjustmentRequest{Asset: "USD"},
					req.policy,
				)
				return err
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`ApplyAdjustment "scoped-acc" "reject"`},
		},
		{
			name: "SetBalanceRealizedPnl",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, err := svc.SetBalanceRealizedPnl(
					ctx, req.account, "USD", "1", req.policy,
				)
				return err
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`SetBalanceRealizedPnl "scoped-acc" "reject"`},
		},
		{
			name: "SubmitOrder",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, err := svc.SubmitOrder(
					ctx, clientOrder(req), backend.SubmitModeHold, req.policy,
				)
				return err
			},
			asked:   []scopeCall{account(scopeClientAccount), policy(scopeClientPolicy)},
			reached: []string{`SubmitOrder "scoped-acc" "reject"`},
		},
		{
			// A drop-copy never asks the scope for a policy: the node receives
			// create for the account the client named and, in the omitted run,
			// for the account the scope supplied, though the scope would answer
			// reject.
			name: "SubmitDropCopyOrder",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, err := svc.SubmitDropCopyOrder(ctx, clientOrder(req), req.policy)
				return err
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`SubmitOrder "scoped-acc" "create"`},
		},
		{
			name: "CheckOrder",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, err := svc.CheckOrder(ctx, domain.OrderProbe{
					Account: req.account, BaseAsset: "AAPL", QuoteAsset: "USD",
				})
				return err
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`CheckOrder "scoped-acc"`},
		},
		{
			name: "ListTradingDestinations",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, err := svc.ListTradingDestinations(ctx, req.account, "")
				return err
			},
			asked: []scopeCall{account(scopeClientAccount)},
			reached: []string{
				`GetAccountState "scoped-acc"`, `ListTradingAccess "scoped-acc"`,
			},
		},
		{
			// The send resolves the order's account once, then submits the
			// resolved account with the hold's own reject policy.
			name: "SendPaperOrder",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				_, err := svc.SendPaperOrder(ctx, clientOrder(req), backend.SendDestination{
					Connection: scopeConnection, Route: "{}",
				})
				return err
			},
			asked: []scopeCall{
				account(scopeClientAccount),
				account(scopeResolvedAccount),
				policy(domain.MissingAccountReject),
			},
			reached: []string{
				`ListTradingAccess "scoped-acc"`,
				`SubmitOrder "scoped-acc" "reject"`,
				`GetVenueOrder "order-1"`,
			},
		},
		{
			name: "PutTradingAccess/account",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.PutTradingAccess(ctx, domain.TradingAccess{
					Account: req.account, Connection: scopeConnection,
				})
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`PutTradingAccess "scoped-acc" ""`},
		},
		{
			name: "PutTradingAccess/account_group",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.PutTradingAccess(ctx, domain.TradingAccess{
					AccountGroup: scopeClientGroup, Connection: scopeConnection,
				})
			},
			asked:   []scopeCall{group},
			reached: []string{`PutTradingAccess "" "client-desk"`},
		},
		{
			name: "RemoveTradingAccess/account",
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.RemoveTradingAccess(ctx, scopeConnection, req.account, "")
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`RemoveTradingAccess "scoped-acc" ""`},
		},
		{
			name: "RemoveTradingAccess/account_group",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.RemoveTradingAccess(ctx, scopeConnection, "", scopeClientGroup)
			},
			asked:   []scopeCall{group},
			reached: []string{`RemoveTradingAccess "" "client-desk"`},
		},
		{
			// An omitted fee account clears it: see
			// TestService_AccountScopeKeepsFeeAccountClear.
			name:       "SetTradingFeeAccount",
			omitClears: true,
			run: func(ctx context.Context, svc *backend.Service, req scopeRequest) error {
				return svc.SetTradingFeeAccount(ctx, scopeConnection, req.account)
			},
			asked:   []scopeCall{account(scopeClientAccount)},
			reached: []string{`SetTradingConnectionFeeAccount "scoped-acc"`},
		},
		{
			name:  "GetOrder",
			owned: true,
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, err := svc.GetOrder(ctx, scopeOwnedOrder.String())
				return err
			},
			asked:   []scopeCall{visible},
			reached: []string{`GetVenueOrder "owned-order"`},
		},
		{
			name:  "ConfirmExecution",
			owned: true,
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, _, err := svc.ConfirmExecution(ctx, scopeOwnedOrder.String())
				return err
			},
			asked: []scopeCall{visible},
			reached: []string{
				`GetVenueOrder "owned-order"`, `ConfirmOrder "owned-order"`,
			},
		},
		{
			name:  "CancelOrder",
			owned: true,
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, _, err := svc.CancelOrder(
					ctx, scopeOwnedOrder.String(), "10", "operator",
				)
				return err
			},
			asked: []scopeCall{visible},
			reached: []string{
				`GetVenueOrder "owned-order"`, `CancelOrder "owned-order"`,
			},
		},
		{
			// The report names no account: the owner is the stored order's.
			name:  "ApplyExecutionReport",
			owned: true,
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, _, err := svc.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
					Order: scopeOwnedOrder, FillQuantity: "1", FillPrice: "100",
					OrderStatus: domain.OrderStatusFilled,
				})
				return err
			},
			asked:   []scopeCall{visible},
			reached: []string{`ApplyExecutionReport "owned-order"`},
		},
		{
			name: "CreateAccount",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, err := svc.CreateAccount(ctx, domain.Account{Code: "new-acc", Title: "New"})
				return err
			},
			asked:   []scopeCall{manage},
			reached: []string{`CreateAccount "new-acc"`},
		},
		{
			name: "UpdateAccount",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, err := svc.UpdateAccount(
					ctx, "old-acc", domain.Account{Code: "new-acc", Title: "Renamed"},
				)
				return err
			},
			asked:   []scopeCall{manage},
			reached: []string{`UpdateAccount "old-acc"`},
		},
		{
			name: "DeleteAccount",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.DeleteAccount(ctx, "old-acc", false)
			},
			asked:   []scopeCall{manage},
			reached: []string{`DeleteAccount "old-acc"`},
		},
		{
			name: "SetAccountGroup",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.SetAccountGroup(ctx, "acc-1", "desk", domain.MissingAccountCreate)
			},
			asked:   []scopeCall{manage},
			reached: []string{`SetAccountGroup "acc-1" "desk" "create"`},
		},
		{
			name: "CreateGroup",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, err := svc.CreateGroup(ctx, domain.AccountGroup{Code: "desk", Title: "Desk"})
				return err
			},
			asked:   []scopeCall{manage},
			reached: []string{`CreateGroup "desk"`},
		},
		{
			name: "UpdateGroup",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, err := svc.UpdateGroup(
					ctx, "desk", domain.AccountGroup{Code: "desk-2", Title: "Desk"},
				)
				return err
			},
			asked:   []scopeCall{manage},
			reached: []string{`UpdateGroup "desk"`},
		},
		{
			name: "SetGroupNotes",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.SetGroupNotes(ctx, "desk", "watch")
			},
			asked:   []scopeCall{manage},
			reached: []string{`SetGroupNotes "desk"`},
		},
		{
			name: "SetGroupCurrency",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.SetGroupCurrency(ctx, "desk", "USD")
			},
			asked:   []scopeCall{manage},
			reached: []string{`SetGroupCurrency "desk"`},
		},
		{
			name: "SetDefaultGroupCurrency",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.SetDefaultGroupCurrency(ctx, "USD")
			},
			asked:   []scopeCall{manage},
			reached: []string{`SetDefaultGroupCurrency`},
		},
		{
			name: "SetGroupBlocked",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.SetGroupBlocked(ctx, "desk", true, "risk")
			},
			asked:   []scopeCall{manage},
			reached: []string{`SetGroupBlocked "desk"`},
		},
		{
			name: "DeleteGroup",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				return svc.DeleteGroup(ctx, "desk", false)
			},
			asked:   []scopeCall{manage},
			reached: []string{`DeleteGroup "desk"`},
		},
	}
}

// assertScopeAsked fails unless the scope was asked exactly want, every time
// with caller.
func assertScopeAsked(
	t *testing.T, scope *recordingScope, caller domain.Caller, want []scopeCall,
) {
	t.Helper()
	expected := make([]scopeCall, 0, len(want))
	for _, call := range want {
		call.caller = caller
		expected = append(expected, call)
	}
	if !reflect.DeepEqual(scope.calls, expected) {
		t.Fatalf("scope asked %+v, want %+v", scope.calls, expected)
	}
}

// TestService_AccountScopeSites drives every operation that names an account
// or a group, addresses an order by its id, or changes the account set
// through a restrictive scope. Allowed, the scope is asked with the request's
// caller and the raw client value, and its answer is what reaches the node.
// Refusing, its error comes back unchanged and nothing reaches the node. A
// site that resolves an account runs again with the account and policy
// omitted: the scope is asked with "" and the account it supplies is what
// reaches the node. A site that takes a policy with a named account refuses
// an omitted policy with today's validation error, though the scope would
// supply a valid one, and nothing reaches the node.
func TestService_AccountScopeSites(t *testing.T) {
	t.Parallel()
	operator := domain.Caller{Source: domain.SourceAPI, Principal: "operator"}
	ctx := auth.ContextWithCaller(context.Background(), operator)
	for _, site := range accountScopeSites() {
		t.Run(site.name+"/allowed", func(t *testing.T) {
			t.Parallel()
			scope, svc, fn := site.prepare(t)
			if err := site.run(ctx, svc, scopeNamedRequest); err != nil {
				t.Fatalf("%s: %v", site.name, err)
			}
			assertScopeAsked(t, scope, operator, site.asked)
			if !reflect.DeepEqual(fn.accountCalls, site.reached) {
				t.Fatalf("node received %q, want %q", fn.accountCalls, site.reached)
			}
		})
		t.Run(site.name+"/refused", func(t *testing.T) {
			t.Parallel()
			scope, svc, fn := site.prepare(t)
			scope.err = errScopeRefused
			// Unchanged means the scope's own error value, not a wrap of it.
			if err := site.run(ctx, svc, scopeNamedRequest); err != errScopeRefused {
				t.Fatalf("%s = %v, want the scope's error unchanged", site.name, err)
			}
			assertScopeAsked(t, scope, operator, site.asked[:1])
			if len(fn.accountCalls) != 0 {
				t.Fatalf("refused request reached the node: %q", fn.accountCalls)
			}
		})
		if site.resolvesAccount() {
			t.Run(site.name+"/omitted", func(t *testing.T) {
				t.Parallel()
				scope, svc, fn := site.prepare(t)
				if err := site.run(ctx, svc, scopeOmittedRequest); err != nil {
					t.Fatalf("%s: %v", site.name, err)
				}
				assertScopeAsked(t, scope, operator, site.omittedAsked())
				if !reflect.DeepEqual(fn.accountCalls, site.reached) {
					t.Fatalf("node received %q, want %q", fn.accountCalls, site.reached)
				}
			})
		}
		if site.takesPolicy() {
			t.Run(site.name+"/policy_required", func(t *testing.T) {
				t.Parallel()
				_, svc, fn := site.prepare(t)
				err := site.run(ctx, svc, scopeRequest{account: scopeClientAccount})
				if !errors.Is(err, domain.ErrInvalid) ||
					domain.ValidationPointer(err) != "/missingAccount" ||
					domain.ValidationConstraint(err) != "required" {
					t.Fatalf("%s = %v, want the required /missingAccount refusal", site.name, err)
				}
				if len(fn.accountCalls) != 0 {
					t.Fatalf("refused request reached the node: %q", fn.accountCalls)
				}
			})
		}
	}
}

// TestService_AccountScopeLeavesAxisFreeLimitsUnowned covers limits whose
// scope has no account axis: the scope is never asked for an account and the
// node receives none, though the scope would supply one.
func TestService_AccountScopeLeavesAxisFreeLimitsUnowned(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		run     func(ctx context.Context, svc *backend.Service) error
		reached string
	}{
		{
			name: "PutRateLimit/broker",
			run: func(ctx context.Context, svc *backend.Service) error {
				return svc.PutRateLimit(ctx, domain.LimitRate{
					Scope: domain.ScopeBroker, Window: time.Second, MaxOrders: 10,
				}, "")
			},
			reached: `PutRateLimit "" "reject"`,
		},
		{
			name: "PutOrderSizeLimit/broker",
			run: func(ctx context.Context, svc *backend.Service) error {
				return svc.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
					Scope: domain.ScopeBroker, MaxQuantity: "10",
				}, "")
			},
			reached: `PutOrderSizeLimit "" "reject"`,
		},
		{
			name: "PutSpotFundsPnlBoundsLimit/global",
			run: func(ctx context.Context, svc *backend.Service) error {
				return svc.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
					Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-1",
				}, "")
			},
			reached: `PutSpotFundsPnlBoundsLimit "" "" "reject"`,
		},
		{
			name: "DeleteLimit/asset",
			run: func(ctx context.Context, svc *backend.Service) error {
				return svc.DeleteLimit(ctx, node.LimitTarget{
					Policy: domain.PolicyRateLimit, Scope: domain.ScopeAsset,
					Asset: "AAPL",
				})
			},
			reached: `DeleteLimit "" ""`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			scope := &recordingScope{
				account: scopeResolvedAccount, policy: scopeResolvedPolicy,
			}
			svc, fn, _ := newScopeTestService(t, scope)
			if err := tc.run(systemCtx(), svc); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			for _, call := range scope.calls {
				if call.method == "Account" {
					t.Fatalf("axis-free limit asked the scope for an account: %+v", scope.calls)
				}
			}
			if want := []string{tc.reached}; !reflect.DeepEqual(fn.accountCalls, want) {
				t.Fatalf("node received %q, want %q", fn.accountCalls, want)
			}
		})
	}
}

// TestService_AccountScopeKeepsFeeAccountClear clears a connection's fee
// account: the empty account names none to resolve, so the scope is not asked
// and the clear reaches the node as a clear.
func TestService_AccountScopeKeepsFeeAccountClear(t *testing.T) {
	t.Parallel()
	scope := &recordingScope{account: scopeResolvedAccount, policy: scopeResolvedPolicy}
	svc, fn, _ := newScopeTestService(t, scope)
	if err := svc.SetTradingFeeAccount(systemCtx(), scopeConnection, ""); err != nil {
		t.Fatalf("clear fee account: %v", err)
	}
	if len(scope.calls) != 0 {
		t.Fatalf("clearing the fee account asked the scope %+v", scope.calls)
	}
	if want := []string{`SetTradingConnectionFeeAccount ""`}; !reflect.DeepEqual(fn.accountCalls, want) {
		t.Fatalf("node received %q, want %q", fn.accountCalls, want)
	}
}

// TestService_AccountScopeAsksForSystemCaller runs the operations venue
// processing performs as the system caller: the scope is asked with that
// caller, and its answer binds the system caller as it binds a client, since
// only the scope decides what the system caller may do.
func TestService_AccountScopeAsksForSystemCaller(t *testing.T) {
	t.Parallel()
	sites := []scopeSite{
		{
			name: "ApplyAdjustment",
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, err := svc.ApplyAdjustment(
					ctx, scopeClientAccount, "", domain.AdjustmentRequest{Asset: "USD"},
					domain.MissingAccountReject,
				)
				return err
			},
			asked: []scopeCall{
				{method: "Account", raw: string(scopeClientAccount)},
				{method: "MissingAccount", raw: string(domain.MissingAccountReject)},
			},
		},
		{
			name:  "ApplyExecutionReport",
			owned: true,
			run: func(ctx context.Context, svc *backend.Service, _ scopeRequest) error {
				_, _, err := svc.ApplyExecutionReport(ctx, domain.ExecutionReportInput{
					Order: scopeOwnedOrder, FillQuantity: "1", FillPrice: "100",
					OrderStatus: domain.OrderStatusFilled,
				})
				return err
			},
			asked: []scopeCall{{method: "Visible", raw: string(scopeOwnerAccount)}},
		},
	}
	for _, site := range sites {
		for _, refused := range []bool{false, true} {
			name := site.name + "/allowed"
			if refused {
				name = site.name + "/refused"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				scope, svc, fn := site.prepare(t)
				if refused {
					scope.err = errScopeRefused
				}
				err := site.run(systemCtx(), svc, scopeRequest{})
				if !refused {
					assertScopeAsked(t, scope, auth.SystemCaller(), site.asked)
					if err != nil || len(fn.accountCalls) != 1 {
						t.Fatalf("%s = %v, node received %q; want it applied", site.name, err, fn.accountCalls)
					}
					return
				}
				assertScopeAsked(t, scope, auth.SystemCaller(), site.asked[:1])
				if err != errScopeRefused || len(fn.accountCalls) != 0 {
					t.Fatalf("%s = %v, node received %q; want the scope's refusal", site.name, err, fn.accountCalls)
				}
			})
		}
	}
}

// scopeRestriction is the list restriction the recording scope answers the
// list tests with; IncludeUnowned is set so a test proves the whole
// restriction reaches the node, not only its account.
var scopeRestriction = domain.AccountRestriction{
	Account: scopeResolvedAccount, IncludeUnowned: true,
}

// listScopeSite is one backend list whose store filter carries the account
// scope's restriction to the node.
type listScopeSite struct {
	name string
	// requested is the raw value the scope is asked with: scopeClientAccount
	// for a list the client names an exact account for, "" for one it filters
	// by a text pattern.
	requested domain.AccountID
	// run lists as a client naming scopeClientAccount, exactly or as a
	// pattern.
	run func(ctx context.Context, svc *backend.Service) error
	// reached is what the node's list received; its zero value when the list
	// did not reach the node.
	reached func(fn *fakeNode) any
	// want is what the node receives when the scope allows the list: the
	// client's own filter, unchanged, plus the scope's restriction.
	want any
}

func listScopeSites() []listScopeSite {
	pattern := store.ExactTextMatcher(scopeClientAccount.String())
	auditFilters := func(fn *fakeNode) any { return fn.auditFilters }
	return []listScopeSite{
		{
			name: "ListAccountRows",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListAccountRows(ctx, store.AccountListFilter{Code: pattern})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastAccountFilter },
			want:    store.AccountListFilter{Code: pattern, Restriction: scopeRestriction},
		},
		{
			name:      "ListOrderRows",
			requested: scopeClientAccount,
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListOrderRows(ctx, store.OrderListFilter{Account: scopeClientAccount})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastOrderFilter },
			want: store.OrderListFilter{
				Account: scopeClientAccount, Restriction: scopeRestriction,
			},
		},
		{
			name:      "ListOrders",
			requested: scopeClientAccount,
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListOrders(ctx, scopeClientAccount, domain.SourceAPI, 10)
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastOrderFilter },
			want: store.OrderListFilter{
				Account: scopeClientAccount, Source: domain.SourceAPI,
				Sort:        store.SortSpec{Column: "at", Descending: true},
				Page:        store.PageSpec{Limit: 10},
				Restriction: scopeRestriction,
			},
		},
		{
			name: "ListTradeRows",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListTradeRows(ctx, store.TradeListFilter{Account: pattern})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastTradeFilter },
			want:    store.TradeListFilter{Account: pattern, Restriction: scopeRestriction},
		},
		{
			name: "ListBalanceRows",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListBalanceRows(ctx, store.BalanceListFilter{Account: pattern})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastBalanceFilter },
			want:    store.BalanceListFilter{Account: pattern, Restriction: scopeRestriction},
		},
		{
			name:      "ListBalances",
			requested: scopeClientAccount,
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListBalances(ctx, scopeClientAccount, "USD")
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastBalanceFilter },
			want: store.BalanceListFilter{
				Account: pattern, Asset: store.ExactTextMatcher("USD"),
				Restriction: scopeRestriction,
			},
		},
		{
			name: "ListAdjustmentRows",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListAdjustmentRows(ctx, store.AdjustmentListFilter{Account: pattern})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastAdjustmentFilter },
			want:    store.AdjustmentListFilter{Account: pattern, Restriction: scopeRestriction},
		},
		{
			name: "ListPolicyRows",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListPolicyRows(ctx, store.PolicyListFilter{Account: pattern})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastPolicyFilter },
			want:    store.PolicyListFilter{Account: pattern, Restriction: scopeRestriction},
		},
		{
			name: "ListAuditRows",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListAuditRows(ctx, store.AuditListFilter{Account: pattern})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastAuditFilter },
			want:    store.AuditListFilter{Account: pattern, Restriction: scopeRestriction},
		},
		{
			name:      "ListAuditFiltered",
			requested: scopeClientAccount,
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListAuditFiltered(
					ctx, domain.AuditFilter{Account: scopeClientAccount}, 10,
				)
				return err
			},
			reached: auditFilters,
			want: []domain.AuditFilter{{
				Account: scopeClientAccount, Restriction: scopeRestriction,
			}},
		},
		{
			// A restricted audit listing is read through the filtered one.
			name: "ListAudit",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListAudit(ctx, 10)
				return err
			},
			reached: auditFilters,
			want:    []domain.AuditFilter{{Restriction: scopeRestriction}},
		},
		{
			name: "ListGroupRows",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListGroupRows(ctx, store.GroupListFilter{Code: pattern})
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastGroupFilter },
			want:    store.GroupListFilter{Code: pattern, Restriction: scopeRestriction},
		},
		{
			name: "ListGroups",
			run: func(ctx context.Context, svc *backend.Service) error {
				_, err := svc.ListGroups(ctx)
				return err
			},
			reached: func(fn *fakeNode) any { return fn.lastGroupFilter },
			want:    store.GroupListFilter{Restriction: scopeRestriction},
		},
	}
}

// TestService_AccountScopeRestrictsLists drives every list whose store filter
// carries the account restriction through a restrictive scope. Allowed, the
// scope is asked once, with the request's caller and the exact account the
// client named ("" for a text pattern), and the node receives the client's
// own filter, unchanged, plus the scope's whole restriction. Refusing, the
// scope's error comes back unchanged and the list never reaches the node.
// Without a caller in the context, the list fails with ErrInvalid before the
// scope is asked or the node is reached.
func TestService_AccountScopeRestrictsLists(t *testing.T) {
	t.Parallel()
	operator := domain.Caller{Source: domain.SourceAPI, Principal: "operator"}
	ctx := auth.ContextWithCaller(context.Background(), operator)
	for _, site := range listScopeSites() {
		asked := []scopeCall{{method: "List", raw: string(site.requested)}}
		t.Run(site.name+"/allowed", func(t *testing.T) {
			t.Parallel()
			scope := &recordingScope{account: scopeResolvedAccount, unowned: true}
			svc, fn, _ := newScopeTestService(t, scope)
			if err := site.run(ctx, svc); err != nil {
				t.Fatalf("%s: %v", site.name, err)
			}
			assertScopeAsked(t, scope, operator, asked)
			if got := site.reached(fn); !reflect.DeepEqual(got, site.want) {
				t.Fatalf("node received %+v, want %+v", got, site.want)
			}
		})
		t.Run(site.name+"/refused", func(t *testing.T) {
			t.Parallel()
			scope := &recordingScope{
				account: scopeResolvedAccount, unowned: true, err: errScopeRefused,
			}
			svc, fn, _ := newScopeTestService(t, scope)
			// Unchanged means the scope's own error value, not a wrap of it.
			if err := site.run(ctx, svc); err != errScopeRefused {
				t.Fatalf("%s = %v, want the scope's error unchanged", site.name, err)
			}
			assertScopeAsked(t, scope, operator, asked)
			if got := site.reached(fn); !reflect.ValueOf(got).IsZero() {
				t.Fatalf("refused list reached the node with %+v", got)
			}
		})
		t.Run(site.name+"/no caller", func(t *testing.T) {
			t.Parallel()
			scope := &recordingScope{account: scopeResolvedAccount, unowned: true}
			svc, fn, _ := newScopeTestService(t, scope)
			if err := site.run(context.Background(), svc); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("%s without a caller = %v, want ErrInvalid", site.name, err)
			}
			if len(scope.calls) != 0 {
				t.Fatalf("scope asked %+v without a caller, want nothing", scope.calls)
			}
			if got := site.reached(fn); !reflect.ValueOf(got).IsZero() {
				t.Fatalf("list without a caller reached the node with %+v", got)
			}
		})
	}
}

// TestService_AccountScopeNarrowsOwnedRowLists covers the lists that narrow
// account-owned rows through the node's exact account. The scope is asked
// with the account the client named: an omitted account lists the restricted
// one, the restricted account lists itself, any other account lists nothing
// without reaching the node, and a refusal comes back unchanged. Without a
// caller in the context, the list fails with ErrInvalid before the scope is
// asked or the node is reached.
func TestService_AccountScopeNarrowsOwnedRowLists(t *testing.T) {
	t.Parallel()
	operator := domain.Caller{Source: domain.SourceAPI, Principal: "operator"}
	ctx := auth.ContextWithCaller(context.Background(), operator)
	sites := []struct {
		name string
		run  func(ctx context.Context, svc *backend.Service, account domain.AccountID) error
		// reached is what the node receives for the restricted account.
		reached string
	}{
		{
			name: "ListTrades",
			run: func(ctx context.Context, svc *backend.Service, account domain.AccountID) error {
				_, err := svc.ListTrades(ctx, account, "", 10)
				return err
			},
			reached: `ListTrades "scoped-acc"`,
		},
		{
			name: "ListAdjustments",
			run: func(ctx context.Context, svc *backend.Service, account domain.AccountID) error {
				_, err := svc.ListAdjustments(ctx, account, "", 10)
				return err
			},
			reached: `ListAdjustments "scoped-acc"`,
		},
		{
			name: "ListAllAdjustments",
			run: func(ctx context.Context, svc *backend.Service, account domain.AccountID) error {
				_, err := svc.ListAllAdjustments(ctx, account, "", 10)
				return err
			},
			reached: `ListAdjustments "scoped-acc"`,
		},
		{
			name: "ListAllOrders",
			run: func(ctx context.Context, svc *backend.Service, account domain.AccountID) error {
				_, err := svc.ListAllOrders(ctx, account, "")
				return err
			},
			reached: `ListAllOrders "scoped-acc"`,
		},
		{
			name: "ListAllTrades",
			run: func(ctx context.Context, svc *backend.Service, account domain.AccountID) error {
				_, err := svc.ListAllTrades(ctx, account, "")
				return err
			},
			reached: `ListAllTrades "scoped-acc"`,
		},
	}
	for _, site := range sites {
		for _, tc := range []struct {
			name    string
			account domain.AccountID
			err     error
			reached []string
		}{
			{name: "omitted", reached: []string{site.reached}},
			{name: "restricted", account: scopeResolvedAccount, reached: []string{site.reached}},
			{name: "other", account: scopeClientAccount},
			{name: "refused", account: scopeClientAccount, err: errScopeRefused},
		} {
			t.Run(site.name+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				scope := &recordingScope{
					account: scopeResolvedAccount, unowned: true, err: tc.err,
				}
				svc, fn, _ := newScopeTestService(t, scope)
				if err := site.run(ctx, svc, tc.account); err != tc.err {
					t.Fatalf("%s(%q) = %v, want %v", site.name, tc.account, err, tc.err)
				}
				assertScopeAsked(t, scope, operator, []scopeCall{
					{method: "List", raw: string(tc.account)},
				})
				if !slices.Equal(fn.accountCalls, tc.reached) {
					t.Fatalf("node received %q, want %q", fn.accountCalls, tc.reached)
				}
			})
		}
		t.Run(site.name+"/no caller", func(t *testing.T) {
			t.Parallel()
			scope := &recordingScope{account: scopeResolvedAccount, unowned: true}
			svc, fn, _ := newScopeTestService(t, scope)
			err := site.run(context.Background(), svc, scopeResolvedAccount)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("%s without a caller = %v, want ErrInvalid", site.name, err)
			}
			if len(scope.calls) != 0 {
				t.Fatalf("scope asked %+v without a caller, want nothing", scope.calls)
			}
			if len(fn.accountCalls) != 0 {
				t.Fatalf("list without a caller reached the node: %q", fn.accountCalls)
			}
		})
	}
}

// TestService_EveryAccountListsReadAsBefore pins the reads the default scope
// keeps: the audit listing stays the node's unfiltered one, and the owned-row
// lists pass the client's account to the node unchanged.
func TestService_EveryAccountListsReadAsBefore(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := systemCtx()
	if _, err := svc.ListAudit(ctx, 10); err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(fn.auditFilters) != 0 {
		t.Fatalf("ListAudit read the filtered listing with %+v", fn.auditFilters)
	}
	if _, err := svc.ListTrades(ctx, "", "", 10); err != nil {
		t.Fatalf("ListTrades: %v", err)
	}
	if _, err := svc.ListAllAdjustments(ctx, "", "", 10); err != nil {
		t.Fatalf("ListAllAdjustments: %v", err)
	}
	if _, err := svc.ListAdjustments(ctx, "acc-1", "", 10); err != nil {
		t.Fatalf("ListAdjustments: %v", err)
	}
	want := []string{`ListTrades ""`, `ListAdjustments ""`, `ListAdjustments "acc-1"`}
	if !slices.Equal(fn.accountCalls, want) {
		t.Fatalf("node received %q, want %q", fn.accountCalls, want)
	}
}

// The narrowing tests' fixture: scopeResolvedAccount is a member of
// scopeVisibleGroup, and scopeHiddenAccount, which the narrowing scope hides,
// of scopeHiddenGroup.
const scopeHiddenAccount domain.AccountID = "hidden-acc"

const (
	scopeVisibleGroup = "desk"
	scopeHiddenGroup  = "ops"
)

// newNarrowingScope returns the restrictive scope of the narrowing tests: it
// restricts every list to scopeResolvedAccount plus the rows with no account
// (scopeRestriction) and hides scopeHiddenAccount.
func newNarrowingScope() *recordingScope {
	return &recordingScope{
		account: scopeResolvedAccount, unowned: true,
		hidden: []domain.AccountID{scopeHiddenAccount},
	}
}

// narrowingAccounts are the narrowing tests' accounts with their groups.
func narrowingAccounts() []domain.Account {
	return []domain.Account{
		{Code: scopeResolvedAccount, GroupCode: scopeVisibleGroup},
		{Code: scopeHiddenAccount, GroupCode: scopeHiddenGroup},
	}
}

// TestService_AccountScopeNarrowsGetGroup reads groups through a restrictive
// scope: the group of the restricted account lists that member alone, while a
// group without it, an empty group and a missing one all answer the same
// not-found error, which names no account. The default scope reads every group
// with every member, an empty group included. Without a caller in the context,
// the read fails with ErrInvalid before the scope is asked or the node is
// reached.
func TestService_AccountScopeNarrowsGetGroup(t *testing.T) {
	t.Parallel()
	members := map[string][]domain.Account{
		scopeVisibleGroup: {{Code: scopeResolvedAccount}, {Code: scopeHiddenAccount}},
		scopeHiddenGroup:  {{Code: scopeHiddenAccount}},
		"empty":           {},
	}
	restricted := func() backend.AccountScope { return newNarrowingScope() }
	every := func() backend.AccountScope { return backend.EveryAccount{} }
	for _, tc := range []struct {
		name  string
		scope func() backend.AccountScope
		group string
		// want is the members read, in order; nil when the group is not found.
		want []domain.AccountID
	}{
		{
			name: "restricted/own group", scope: restricted, group: scopeVisibleGroup,
			want: []domain.AccountID{scopeResolvedAccount},
		},
		{name: "restricted/other group", scope: restricted, group: scopeHiddenGroup},
		{name: "restricted/empty group", scope: restricted, group: "empty"},
		{name: "restricted/missing group", scope: restricted, group: "missing"},
		{
			name: "every account/own group", scope: every, group: scopeVisibleGroup,
			want: []domain.AccountID{scopeResolvedAccount, scopeHiddenAccount},
		},
		{
			name: "every account/other group", scope: every, group: scopeHiddenGroup,
			want: []domain.AccountID{scopeHiddenAccount},
		},
		{
			name: "every account/empty group", scope: every, group: "empty",
			want: []domain.AccountID{},
		},
		{name: "every account/missing group", scope: every, group: "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, fn, _ := newScopeTestService(t, tc.scope())
			fn.groupMembers = members
			group, accounts, err := svc.GetGroup(systemCtx(), tc.group)
			if tc.want == nil {
				want := fmt.Sprintf("group %q: %v", tc.group, domain.ErrNotFound)
				if !errors.Is(err, domain.ErrNotFound) || err.Error() != want {
					t.Fatalf("GetGroup(%q) = %v, want %q", tc.group, err, want)
				}
				return
			}
			if err != nil || group.Code != tc.group {
				t.Fatalf("GetGroup(%q) = %+v, %v", tc.group, group, err)
			}
			got := make([]domain.AccountID, 0, len(accounts))
			for _, account := range accounts {
				got = append(got, account.Code)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("GetGroup(%q) members = %q, want %q", tc.group, got, tc.want)
			}
		})
	}
	t.Run("refused", func(t *testing.T) {
		t.Parallel()
		scope := newNarrowingScope()
		scope.err = errScopeRefused
		svc, fn, _ := newScopeTestService(t, scope)
		fn.groupMembers = members
		// Unchanged means the scope's own error value, not a wrap of it.
		if _, _, err := svc.GetGroup(systemCtx(), scopeVisibleGroup); err != errScopeRefused {
			t.Fatalf("GetGroup = %v, want the scope's error unchanged", err)
		}
	})
	t.Run("no caller", func(t *testing.T) {
		t.Parallel()
		scope := newNarrowingScope()
		svc, fn, _ := newScopeTestService(t, scope)
		fn.groupMembers = members
		_, _, err := svc.GetGroup(context.Background(), scopeVisibleGroup)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("GetGroup without a caller = %v, want ErrInvalid", err)
		}
		if len(scope.calls) != 0 {
			t.Fatalf("scope asked %+v without a caller, want nothing", scope.calls)
		}
		if len(fn.accountCalls) != 0 {
			t.Fatalf("read without a caller reached the node: %q", fn.accountCalls)
		}
	})
}

// TestService_AccountScopeNarrowsTrading reads the trading status through a
// restrictive scope: a connection keeps the grants of the restricted account
// and of its group, drops those of a hidden account and of another group, and
// reports a hidden fee account as no fee account. The default scope reports
// every grant and fee account.
func TestService_AccountScopeNarrowsTrading(t *testing.T) {
	t.Parallel()
	const second domain.ExternalID = "connection-2"
	grants := []domain.TradingAccess{
		{Account: scopeResolvedAccount, Connection: scopeConnection},
		{Account: scopeHiddenAccount, Connection: scopeConnection},
		{AccountGroup: scopeVisibleGroup, Connection: scopeConnection},
		{AccountGroup: scopeHiddenGroup, Connection: scopeConnection},
	}
	rows := []store.TradingAccessListRow{
		{Access: grants[0], Group: scopeVisibleGroup},
		{Access: grants[1], Group: scopeHiddenGroup},
		{Access: grants[2]},
		{Access: grants[3]},
	}
	// connection is what a test reads of one connection.
	type connection struct {
		fee    domain.AccountID
		access []store.TradingAccessListRow
	}
	for _, tc := range []struct {
		name  string
		scope backend.AccountScope
		want  map[domain.ExternalID]connection
	}{
		{
			name:  "restricted",
			scope: newNarrowingScope(),
			want: map[domain.ExternalID]connection{
				scopeConnection: {
					fee:    scopeResolvedAccount,
					access: []store.TradingAccessListRow{rows[0], rows[2]},
				},
				second: {},
			},
		},
		{
			name:  "every account",
			scope: backend.EveryAccount{},
			want: map[domain.ExternalID]connection{
				scopeConnection: {fee: scopeResolvedAccount, access: rows},
				second:          {fee: scopeHiddenAccount},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, fn, _ := newScopeTestService(t, tc.scope)
			fn.accounts = narrowingAccounts()
			fn.tradingAccess = grants
			first := fn.tradingConnections[0]
			first.FeeAccount = scopeResolvedAccount
			hiddenFee := first
			hiddenFee.ExternalID = second
			hiddenFee.FeeAccount = scopeHiddenAccount
			fn.tradingConnections = []domain.TradingConnection{first, hiddenFee}
			status, err := svc.GetTrading(systemCtx())
			if err != nil {
				t.Fatalf("GetTrading: %v", err)
			}
			got := make(map[domain.ExternalID]connection, len(status.Connections))
			for _, c := range status.Connections {
				got[c.ExternalID] = connection{fee: c.FeeAccount, access: c.Access}
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("connections = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestService_AccountScopeNarrowsLimits lists barriers through a restrictive
// scope: it keeps the barriers of the restricted account, those with no
// account and the group barriers of its group, and drops those of a hidden
// account and of another group. A named account reaches the node as the scope
// resolves it, an omitted one as every barrier. The default scope lists every
// barrier as the node returns it, and a refusal comes back unchanged. Without a
// caller in the context, the list fails with ErrInvalid before the scope is
// asked or the node is reached.
func TestService_AccountScopeNarrowsLimits(t *testing.T) {
	t.Parallel()
	limits := node.AccountLimits{
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeAccount, Account: scopeResolvedAccount},
			{Scope: domain.ScopeAccount, Account: scopeHiddenAccount},
			{Scope: domain.ScopeBroker},
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{
				Scope:   domain.ScopeAccountSettlementAsset,
				Account: scopeHiddenAccount, Asset: "USD",
			},
			{Scope: domain.ScopeSettlementAsset, Asset: "USD"},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{Scope: domain.ScopeAccount, Account: scopeResolvedAccount},
			{Scope: domain.ScopeAccount, Account: scopeHiddenAccount},
			{Scope: domain.ScopeAccountGroup, AccountGroup: scopeVisibleGroup},
			{Scope: domain.ScopeAccountGroup, AccountGroup: scopeHiddenGroup},
			{Scope: domain.ScopeGlobal},
		},
	}
	narrowed := node.AccountLimits{
		RateLimits: []domain.LimitRate{limits.RateLimits[0], limits.RateLimits[2]},
		OrderSizeLimits: []domain.LimitOrderSize{
			limits.OrderSizeLimits[1],
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			limits.SpotFundsPnlBoundsLimits[0],
			limits.SpotFundsPnlBoundsLimits[2],
			limits.SpotFundsPnlBoundsLimits[4],
		},
	}
	for _, tc := range []struct {
		name    string
		scope   backend.AccountScope
		account domain.AccountID
		want    node.AccountLimits
		reached string
	}{
		{
			name: "restricted/omitted", scope: newNarrowingScope(),
			want: narrowed, reached: `ListLimits ""`,
		},
		{
			name: "restricted/named", scope: newNarrowingScope(),
			account: scopeClientAccount, want: narrowed,
			reached: `ListLimits "scoped-acc"`,
		},
		{
			name: "every account/omitted", scope: backend.EveryAccount{},
			want: limits, reached: `ListLimits ""`,
		},
		{
			name: "every account/named", scope: backend.EveryAccount{},
			account: scopeClientAccount, want: limits,
			reached: `ListLimits "client-acc"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, fn, _ := newScopeTestService(t, tc.scope)
			fn.accounts = narrowingAccounts()
			fn.limits = limits
			got, err := svc.ListLimits(systemCtx(), tc.account)
			if err != nil {
				t.Fatalf("ListLimits(%q): %v", tc.account, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ListLimits(%q) = %+v, want %+v", tc.account, got, tc.want)
			}
			if want := []string{tc.reached}; !slices.Equal(fn.accountCalls, want) {
				t.Fatalf("node received %q, want %q", fn.accountCalls, want)
			}
		})
	}
	for _, account := range []domain.AccountID{"", scopeClientAccount} {
		t.Run("refused/"+string(account), func(t *testing.T) {
			t.Parallel()
			scope := newNarrowingScope()
			scope.err = errScopeRefused
			svc, fn, _ := newScopeTestService(t, scope)
			fn.limits = limits
			// Unchanged means the scope's own error value, not a wrap of it.
			if _, err := svc.ListLimits(systemCtx(), account); err != errScopeRefused {
				t.Fatalf("ListLimits(%q) = %v, want the scope's error unchanged", account, err)
			}
			if len(fn.accountCalls) != 0 {
				t.Fatalf("refused list reached the node: %q", fn.accountCalls)
			}
		})
		t.Run("no caller/"+string(account), func(t *testing.T) {
			t.Parallel()
			scope := newNarrowingScope()
			svc, fn, _ := newScopeTestService(t, scope)
			fn.limits = limits
			if _, err := svc.ListLimits(context.Background(), account); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("ListLimits(%q) without a caller = %v, want ErrInvalid", account, err)
			}
			if len(scope.calls) != 0 {
				t.Fatalf("scope asked %+v without a caller, want nothing", scope.calls)
			}
			if len(fn.accountCalls) != 0 {
				t.Fatalf("list without a caller reached the node: %q", fn.accountCalls)
			}
		})
	}
}

// TestService_AccountScopeNarrowsOverview builds the overview through a
// restrictive scope: the order counts take the scope's list restriction, the
// account, group and limit tallies come from narrowed reads, and the activity
// feed is read through the restricted audit, order and adjustment lists. The
// default scope counts with the zero restriction and reads as before.
func TestService_AccountScopeNarrowsOverview(t *testing.T) {
	t.Parallel()
	limits := node.AccountLimits{
		RateLimits: []domain.LimitRate{
			{Scope: domain.ScopeAccount, Account: scopeResolvedAccount},
			{Scope: domain.ScopeAccount, Account: scopeHiddenAccount},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			{Scope: domain.ScopeAccountGroup, AccountGroup: scopeVisibleGroup},
			{Scope: domain.ScopeAccountGroup, AccountGroup: scopeHiddenGroup},
		},
	}
	for _, tc := range []struct {
		name  string
		scope backend.AccountScope
		// restriction is what every list and count carries.
		restriction domain.AccountRestriction
		accounts    int
		limits      int
		// audit is the filtered audit reads; the zero restriction reads the
		// unfiltered listing.
		audit   []domain.AuditFilter
		reached []string
	}{
		{
			name:        "restricted",
			scope:       newNarrowingScope(),
			restriction: scopeRestriction,
			accounts:    1,
			limits:      2,
			audit:       []domain.AuditFilter{{Restriction: scopeRestriction}},
			reached:     []string{`ListLimits ""`, `ListAdjustments "scoped-acc"`},
		},
		{
			name:     "every account",
			scope:    backend.EveryAccount{},
			accounts: 2,
			limits:   4,
			reached:  []string{`ListLimits ""`, `ListAdjustments ""`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, fn, _ := newScopeTestService(t, tc.scope)
			fn.accounts = narrowingAccounts()
			fn.limits = limits
			overview, err := svc.Overview(systemCtx(), time.Now().UTC())
			if err != nil {
				t.Fatalf("Overview: %v", err)
			}
			if overview.Counts.Accounts != tc.accounts || overview.Counts.Limits != tc.limits {
				t.Fatalf("counts = %+v, want %d accounts and %d limits",
					overview.Counts, tc.accounts, tc.limits)
			}
			counts := []domain.AccountRestriction{tc.restriction, tc.restriction, tc.restriction}
			if !slices.Equal(fn.countRestrictions, counts) {
				t.Fatalf("order counts received %+v, want %+v", fn.countRestrictions, counts)
			}
			if fn.lastGroupFilter.Restriction != tc.restriction ||
				fn.lastOrderFilter.Restriction != tc.restriction {
				t.Fatalf("group list %+v, order list %+v, want restriction %+v",
					fn.lastGroupFilter, fn.lastOrderFilter, tc.restriction)
			}
			if !reflect.DeepEqual(fn.auditFilters, tc.audit) {
				t.Fatalf("audit read %+v, want %+v", fn.auditFilters, tc.audit)
			}
			if !slices.Equal(fn.accountCalls, tc.reached) {
				t.Fatalf("node received %q, want %q", fn.accountCalls, tc.reached)
			}
		})
	}
}

// TestService_AccountScopeNarrowsBusinessCSV exports business entities through
// a restrictive scope: every entity is read through a list carrying the
// scope's restriction, so the order and trade files keep only the restricted
// account's rows, and a named group is checked with the scope first. The
// default scope exports every row.
func TestService_AccountScopeNarrowsBusinessCSV(t *testing.T) {
	t.Parallel()
	export := func(
		t *testing.T, svc *backend.Service, entity businesscsv.Entity,
		filter businesscsv.ExportFilter,
	) (string, error) {
		t.Helper()
		file, err := svc.ExportBusinessCSV(systemCtx(), backend.BusinessCSVExportRequest{
			Entity: entity, Delimiter: businesscsv.DelimiterComma, Filter: filter,
		})
		return string(file.Body), err
	}
	for _, tc := range []struct {
		name  string
		scope backend.AccountScope
		// rows are the order and trade ids each file must hold and absent
		// those it must not.
		rows, absent []string
		reached      []string
	}{
		{
			name:    "restricted",
			scope:   newNarrowingScope(),
			rows:    []string{"order-scoped", "trade-scoped"},
			absent:  []string{"order-hidden", "trade-hidden"},
			reached: []string{`ListAllOrders "scoped-acc"`, `ListAllTrades "scoped-acc"`},
		},
		{
			name:  "every account",
			scope: backend.EveryAccount{},
			rows: []string{
				"order-scoped", "order-hidden", "trade-scoped", "trade-hidden",
			},
			reached: []string{`ListAllOrders ""`, `ListAllTrades ""`},
		},
	} {
		t.Run(tc.name+"/orders and trades", func(t *testing.T) {
			t.Parallel()
			svc, fn, _ := newScopeTestService(t, tc.scope)
			fn.allOrders = []domain.Order{
				{ExternalID: "order-scoped", Account: scopeResolvedAccount},
				{ExternalID: "order-hidden", Account: scopeHiddenAccount},
			}
			fn.allTrades = []domain.Trade{
				{ExternalID: "trade-scoped", Account: scopeResolvedAccount},
				{ExternalID: "trade-hidden", Account: scopeHiddenAccount},
			}
			var body strings.Builder
			for _, entity := range []businesscsv.Entity{
				businesscsv.EntityOrders, businesscsv.EntityTrades,
			} {
				file, err := export(t, svc, entity, businesscsv.ExportFilter{})
				if err != nil {
					t.Fatalf("export %s: %v", entity, err)
				}
				body.WriteString(file)
			}
			for _, id := range tc.rows {
				if !strings.Contains(body.String(), id) {
					t.Fatalf("export %q misses %s", body.String(), id)
				}
			}
			for _, id := range tc.absent {
				if strings.Contains(body.String(), id) {
					t.Fatalf("export %q holds %s", body.String(), id)
				}
			}
			if !slices.Equal(fn.accountCalls, tc.reached) {
				t.Fatalf("node received %q, want %q", fn.accountCalls, tc.reached)
			}
		})
	}
	t.Run("restricted/every entity reads a restricted list", func(t *testing.T) {
		t.Parallel()
		svc, fn, _ := newScopeTestService(t, newNarrowingScope())
		for _, entity := range []businesscsv.Entity{
			businesscsv.EntityAccountGroups, businesscsv.EntityAccounts,
			businesscsv.EntityPositions,
		} {
			if _, err := export(t, svc, entity, businesscsv.ExportFilter{}); err != nil {
				t.Fatalf("export %s: %v", entity, err)
			}
		}
		if fn.lastGroupFilter.Restriction != scopeRestriction ||
			fn.lastAccountFilter.Restriction != scopeRestriction ||
			fn.lastBalanceFilter.Restriction != scopeRestriction {
			t.Fatalf("group %+v, account %+v and balance %+v lists, want restriction %+v",
				fn.lastGroupFilter, fn.lastAccountFilter, fn.lastBalanceFilter,
				scopeRestriction)
		}
	})
	t.Run("named group is asked first", func(t *testing.T) {
		t.Parallel()
		filter := businesscsv.ExportFilter{GroupCode: scopeVisibleGroup, GroupCodeSet: true}
		group := scopeCall{method: "Group", raw: scopeVisibleGroup}

		allowed := newNarrowingScope()
		svc, _, _ := newScopeTestService(t, allowed)
		if _, err := export(t, svc, businesscsv.EntityAccounts, filter); err != nil {
			t.Fatalf("export accounts: %v", err)
		}
		if len(allowed.calls) == 0 || allowed.calls[0].method != group.method ||
			allowed.calls[0].raw != group.raw {
			t.Fatalf("scope asked %+v, want %+v first", allowed.calls, group)
		}

		refusing := newNarrowingScope()
		refusing.err = errScopeRefused
		svc, fn, _ := newScopeTestService(t, refusing)
		// Unchanged means the scope's own error value, not a wrap of it.
		if _, err := export(t, svc, businesscsv.EntityAccounts, filter); err != errScopeRefused {
			t.Fatalf("export accounts = %v, want the scope's error unchanged", err)
		}
		assertScopeAsked(t, refusing, auth.SystemCaller(), []scopeCall{group})
		if len(fn.auditCalls) != 0 {
			t.Fatalf("refused export was audited: %+v", fn.auditCalls)
		}
	})
}
