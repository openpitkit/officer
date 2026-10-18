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

package node

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// agentCaller is an agent over MCP: the caller that may only tighten risk
// state.
var agentCaller = domain.Caller{
	Source: domain.SourceMCP, Principal: domain.PrincipalOperator,
}

// operatorCallers are the callers a group delete serves as it always did: the
// panel, the REST API and the system.
var operatorCallers = map[string]domain.Caller{
	"panel":  {Source: domain.SourcePanel, Principal: domain.PrincipalOperator},
	"api":    testCaller,
	"system": {Source: domain.SourceSystem},
}

// newResolvingEngine returns a fake engine that refuses an identity it was
// neither built nor published with.
func newResolvingEngine() *fakeEngine {
	eng := newFakeEngine()
	eng.enforceResolver = true
	return eng
}

// newDeskNode builds a node holding the unblocked group desk.
func newDeskNode(t *testing.T) (*localNode, *rebuildProbe) {
	t.Helper()
	n, probe := newRebuildProbeNode(t, newResolvingEngine())
	if _, err := n.CreateGroup(
		context.Background(), domain.AccountGroup{Code: "desk"}, testCaller,
	); err != nil {
		t.Fatalf("CreateGroup(desk): %v", err)
	}
	return n, probe
}

// storedGroup reads group code from the node's store.
func storedGroup(t *testing.T, n *localNode, code string) (domain.AccountGroup, bool) {
	t.Helper()
	group, ok, err := n.realm.GetGroup(context.Background(), code)
	if err != nil {
		t.Fatalf("GetGroup(%s): %v", code, err)
	}
	return group, ok
}

// requireLoosening fails the test unless err refuses a loosening.
func requireLoosening(t *testing.T, call string, err error) {
	t.Helper()
	if !errors.Is(err, domain.ErrLoosening) {
		t.Fatalf("%s = %v, want ErrLoosening", call, err)
	}
}

// refusalLeavesNoTrace records the node's audit trail, its live engine and its
// build count, and returns a check that fails the test when a refused call
// moved any of them.
func refusalLeavesNoTrace(
	t *testing.T, n *localNode, probe *rebuildProbe,
) func() {
	t.Helper()
	ctx := context.Background()
	audit, err := n.ListAudit(ctx, 100)
	if err != nil {
		t.Fatalf("ListAudit before the refusal: %v", err)
	}
	live, builds := n.currentEngine(), probe.builds
	return func() {
		t.Helper()
		after, err := n.ListAudit(ctx, 100)
		if err != nil {
			t.Fatalf("ListAudit after the refusal: %v", err)
		}
		if !reflect.DeepEqual(after, audit) {
			t.Fatalf(
				"refusal changed the audit trail: before=%+v after=%+v",
				audit, after,
			)
		}
		if probe.builds != builds || n.currentEngine() != live {
			t.Fatalf(
				"refusal touched the engine: builds %d -> %d, replaced=%v",
				builds, probe.builds, n.currentEngine() != live,
			)
		}
	}
}

// TestLocalNode_AgentDeleteGroupRefusesBlockedGroup refuses an agent's delete
// of a blocked group, with or without force, because the block would stop
// applying to its members, and changes nothing: the group stays blocked and
// its members stay in it, blocked through it. Every other caller deletes the
// group as before, and its members leave it unblocked.
func TestLocalNode_AgentDeleteGroupRefusesBlockedGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	members := []domain.AccountID{"member-a", "member-b"}
	newBlockedNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		n, probe := newDeskNode(t)
		for _, code := range members {
			if _, err := n.CreateAccount(ctx, domain.Account{
				Code: code, GroupCode: "desk",
			}, testCaller); err != nil {
				t.Fatalf("CreateAccount(%s): %v", code, err)
			}
		}
		if err := n.SetGroupBlocked(ctx, "desk", true, "risk", testCaller); err != nil {
			t.Fatalf("SetGroupBlocked: %v", err)
		}
		prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
		return n, probe
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newBlockedNode(t)
		unchanged := refusalLeavesNoTrace(t, n, probe)
		for _, force := range []bool{false, true} {
			requireLoosening(t, fmt.Sprintf("DeleteGroup(force=%v)", force),
				n.DeleteGroup(ctx, "desk", force, agentCaller))
		}
		unchanged()
		group, ok := storedGroup(t, n, "desk")
		if !ok || !group.Blocked {
			t.Fatalf("group after the refusal = %+v ok=%v, want it blocked", group, ok)
		}
		for _, code := range members {
			account, ok, err := n.realm.GetAccount(ctx, code)
			if err != nil || !ok {
				t.Fatalf("GetAccount(%s): ok=%v err=%v", code, ok, err)
			}
			block := domain.ResolveAccountBlock(account, group)
			if account.GroupCode != "desk" || !block.Blocked || !block.GroupBlocked {
				t.Fatalf(
					"member %s after the refusal = %+v with block %+v, want it in desk and blocked through it",
					code, account, block,
				)
			}
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newBlockedNode(t)
			if err := n.DeleteGroup(ctx, "desk", false, caller); err != nil {
				t.Fatalf("DeleteGroup: %v", err)
			}
			if _, ok := storedGroup(t, n, "desk"); ok {
				t.Fatal("group desk is still stored after the delete")
			}
			for _, code := range members {
				account, ok, err := n.realm.GetAccount(ctx, code)
				if err != nil || !ok || account.GroupCode != "" || account.Blocked {
					t.Fatalf(
						"member %s after the delete = %+v ok=%v err=%v, want it detached and unblocked",
						code, account, ok, err,
					)
				}
			}
		})
	}
}

// TestLocalNode_AgentGroupDeleteRefusalPrecedesCurrencyGuard refuses an agent's
// delete of a group before the currency guard, which would refuse it too and
// audits its own refusal, so the agent's refusal leaves no audit row. It does
// so for each tighten-only refusal: a blocked group, an unblocked group
// carrying a group-scoped Spot Funds P&L bound, and an unblocked group without
// a bound while a rate-limit barrier is configured. The operator's delete of
// the same group is refused by the currency guard, which proves that the guard
// would have refused it.
func TestLocalNode_AgentGroupDeleteRefusalPrecedesCurrencyGuard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, loosening := range map[string]func(*testing.T, *localNode, *rebuildProbe){
		"blocked group": func(t *testing.T, n *localNode, _ *rebuildProbe) {
			t.Helper()
			if err := n.SetGroupBlocked(ctx, "desk", true, "risk", testCaller); err != nil {
				t.Fatalf("SetGroupBlocked: %v", err)
			}
		},
		"group-scoped spot funds pnl bound": func(t *testing.T, n *localNode, _ *rebuildProbe) {
			t.Helper()
			if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
				Scope: domain.ScopeAccountGroup, AccountGroup: "desk",
				Currency: "EUR", UpperBound: "10",
			}, domain.MissingAccountCreate, testCaller); err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
			}
		},
		"rate barrier": func(t *testing.T, n *localNode, probe *rebuildProbe) {
			t.Helper()
			// The first rate barrier replaces the engine.
			prepareGroupDeleteRebuild(n, probe, newFakeEngine())
			if _, err := n.PutRateLimit(ctx, rateLimit(
				domain.ScopeBroker, "", "", 100, time.Second,
			), domain.MissingAccountCreate, testCaller); err != nil {
				t.Fatalf("PutRateLimit: %v", err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newRebuildProbeNode(t, newFakeEngine())
			createCurrencyAssets(t, n.realm, "USD", "EUR")
			if err := n.SetDefaultGroupCurrency(ctx, "USD", testCaller); err != nil {
				t.Fatalf("SetDefaultGroupCurrency: %v", err)
			}
			if _, err := n.CreateGroup(ctx, domain.AccountGroup{
				Code: "desk", Currency: "EUR",
			}, testCaller); err != nil {
				t.Fatalf("CreateGroup: %v", err)
			}
			if _, err := n.CreateAccount(ctx, domain.Account{
				Code: "member-a", GroupCode: "desk",
			}, testCaller); err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			// The open balance is protected state, and leaving desk would move
			// the member's effective currency from EUR to the default group's
			// USD.
			if err := n.realm.UpsertBalance(ctx, domain.Balance{
				Account: "member-a", Asset: "USD", Available: "1",
			}); err != nil {
				t.Fatalf("UpsertBalance: %v", err)
			}
			loosening(t, n, probe)
			prepareGroupDeleteRebuild(n, probe, newFakeEngine())

			unchanged := refusalLeavesNoTrace(t, n, probe)
			requireLoosening(t, "agent DeleteGroup",
				n.DeleteGroup(ctx, "desk", false, agentCaller))
			unchanged()

			var currencyBlocked domain.CurrencyChangeBlockedError
			if err := n.DeleteGroup(ctx, "desk", false, testCaller); !errors.As(err, &currencyBlocked) {
				t.Fatalf("operator DeleteGroup = %v, want the currency guard's refusal", err)
			}
		})
	}
}

// TestLocalNode_AgentDeleteGroupRefusesGroupPnlBound refuses an agent's delete
// of a group carrying a group-scoped Spot Funds P&L bound, with or without
// force, and keeps the bound. Every other caller is refused with has_dependents
// without force and deletes the group and its bound with force, as before.
func TestLocalNode_AgentDeleteGroupRefusesGroupPnlBound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newBoundNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		n, probe := newDeskNode(t)
		if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeAccountGroup, AccountGroup: "desk",
			Currency: "USD", UpperBound: "10",
		}, domain.MissingAccountCreate, testCaller); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
		prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
		return n, probe
	}
	boundStored := func(t *testing.T, n *localNode) bool {
		t.Helper()
		limits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
		if err != nil {
			t.Fatalf("ListSpotFundsPnlBoundsLimits: %v", err)
		}
		return len(limits) == 1 && isGroupSpotFundsPnlBoundsLimit(limits[0], "desk")
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newBoundNode(t)
		unchanged := refusalLeavesNoTrace(t, n, probe)
		for _, force := range []bool{false, true} {
			requireLoosening(t, fmt.Sprintf("DeleteGroup(force=%v)", force),
				n.DeleteGroup(ctx, "desk", force, agentCaller))
		}
		unchanged()
		if _, ok := storedGroup(t, n, "desk"); !ok {
			t.Fatal("group desk is gone after the refusal")
		}
		if !boundStored(t, n) {
			t.Fatal("the group's bound is gone after the refusal")
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newBoundNode(t)
			if err := n.DeleteGroup(ctx, "desk", false, caller); !errors.Is(err, domain.ErrHasDependents) {
				t.Fatalf("DeleteGroup without force = %v, want ErrHasDependents", err)
			}
			if !boundStored(t, n) {
				t.Fatal("the group's bound is gone after the refused delete")
			}
			prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
			if err := n.DeleteGroup(ctx, "desk", true, caller); err != nil {
				t.Fatalf("DeleteGroup with force: %v", err)
			}
			if _, ok := storedGroup(t, n, "desk"); ok {
				t.Fatal("group desk is still stored after the delete")
			}
			limits, err := n.realm.ListSpotFundsPnlBoundsLimits(ctx, "")
			if err != nil || len(limits) != 0 {
				t.Fatalf("bounds after the delete = %+v err=%v, want none", limits, err)
			}
		})
	}
}

// TestLocalNode_AgentDeleteGroupRefusedWhileRateBarrierExists refuses an
// agent's delete of an unblocked group without a bound while any rate-limit
// barrier is configured, here a broker one that has nothing to do with the
// group, because the engine rebuild would reset every rate window. Every other
// caller still deletes the group. Without a rate barrier the agent deletes it,
// also while another group carries a bound.
func TestLocalNode_AgentDeleteGroupRefusedWhileRateBarrierExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newRateNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		n, probe := newDeskNode(t)
		// The first rate barrier replaces the engine.
		prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
		if _, err := n.PutRateLimit(ctx, rateLimit(
			domain.ScopeBroker, "", "", 100, time.Second,
		), domain.MissingAccountCreate, testCaller); err != nil {
			t.Fatalf("PutRateLimit: %v", err)
		}
		prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
		return n, probe
	}
	rateBarrierStored := func(t *testing.T, n *localNode) bool {
		t.Helper()
		limits, err := n.realm.ListRateLimits(ctx, "")
		if err != nil {
			t.Fatalf("ListRateLimits: %v", err)
		}
		return len(limits) == 1
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newRateNode(t)
		unchanged := refusalLeavesNoTrace(t, n, probe)
		requireLoosening(t, "DeleteGroup",
			n.DeleteGroup(ctx, "desk", false, agentCaller))
		unchanged()
		if _, ok := storedGroup(t, n, "desk"); !ok {
			t.Fatal("group desk is gone after the refusal")
		}
		if !rateBarrierStored(t, n) {
			t.Fatal("the rate barrier is gone after the refusal")
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newRateNode(t)
			if err := n.DeleteGroup(ctx, "desk", false, caller); err != nil {
				t.Fatalf("DeleteGroup: %v", err)
			}
			if _, ok := storedGroup(t, n, "desk"); ok {
				t.Fatal("group desk is still stored after the delete")
			}
			if !rateBarrierStored(t, n) {
				t.Fatal("the rate barrier is gone after the group delete")
			}
		})
	}
	t.Run("agent without a rate barrier", func(t *testing.T) {
		t.Parallel()
		n, probe := newDeskNode(t)
		if _, err := n.CreateGroup(
			ctx, domain.AccountGroup{Code: "other"}, testCaller,
		); err != nil {
			t.Fatalf("CreateGroup(other): %v", err)
		}
		if _, err := n.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeAccountGroup, AccountGroup: "other",
			Currency: "USD", UpperBound: "10",
		}, domain.MissingAccountCreate, testCaller); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
		prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
		if err := n.DeleteGroup(ctx, "desk", false, agentCaller); err != nil {
			t.Fatalf("DeleteGroup: %v", err)
		}
		if _, ok := storedGroup(t, n, "desk"); ok {
			t.Fatal("group desk is still stored after the delete")
		}
		if _, ok := storedGroup(t, n, "other"); !ok {
			t.Fatal("group other is gone after the delete of desk")
		}
	})
}

// failingBarrierReadsRealm fails the next read of the rate barriers with
// rateErr and the next read of the Spot Funds P&L bounds with boundsErr,
// whichever is set, and passes every later read through.
type failingBarrierReadsRealm struct {
	store.RealmStore
	rateErr, boundsErr error
}

func (r *failingBarrierReadsRealm) ListRateLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.LimitRate, error) {
	if err := r.rateErr; err != nil {
		r.rateErr = nil
		return nil, err
	}
	return r.RealmStore.ListRateLimits(ctx, account)
}

func (r *failingBarrierReadsRealm) ListSpotFundsPnlBoundsLimits(
	ctx context.Context, account domain.AccountID,
) ([]domain.LimitSpotFundsPnlBounds, error) {
	if err := r.boundsErr; err != nil {
		r.boundsErr = nil
		return nil, err
	}
	return r.RealmStore.ListSpotFundsPnlBoundsLimits(ctx, account)
}

// TestLocalNode_AgentDeleteGroupFailsOnUnreadableBarriers answers an agent's
// delete with the failure of a barrier read the decision needs, instead of
// deciding without it, and deletes nothing. Only that read fails, so a
// decision that went on without it would delete the group.
func TestLocalNode_AgentDeleteGroupFailsOnUnreadableBarriers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	readErr := errors.New("barrier read failure")
	for name, realm := range map[string]failingBarrierReadsRealm{
		"rate barriers":         {rateErr: readErr},
		"spot funds pnl bounds": {boundsErr: readErr},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newDeskNode(t)
			prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
			realm.RealmStore = n.realm
			n.realm = &realm
			unchanged := refusalLeavesNoTrace(t, n, probe)
			if err := n.DeleteGroup(ctx, "desk", false, agentCaller); !errors.Is(err, readErr) {
				t.Fatalf("DeleteGroup = %v, want the read failure", err)
			}
			unchanged()
			if _, ok := storedGroup(t, n, "desk"); !ok {
				t.Fatal("group desk is gone after the failed read")
			}
		})
	}
}

// newSubjectNode builds a node holding account acc, a member of the unblocked
// group desk, and asset AAPL, applies each of protections to its store, and
// prepares the engine rebuild a delete makes.
func newSubjectNode(
	t *testing.T,
	protections ...func(context.Context, store.RealmStore) error,
) (*localNode, *rebuildProbe) {
	t.Helper()
	ctx := context.Background()
	n, probe := newDeskNode(t)
	if _, err := n.CreateAccount(ctx, domain.Account{
		Code: "acc", GroupCode: "desk",
	}, testCaller); err != nil {
		t.Fatalf("CreateAccount(acc): %v", err)
	}
	for _, protect := range protections {
		if err := protect(ctx, n.realm); err != nil {
			t.Fatalf("protect: %v", err)
		}
	}
	prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
	return n, probe
}

// accountProtections protect account acc of newSubjectNode in each way an
// agent may not shed by deleting the account or changing its code, since an
// account recreated under the code has no block, no group and no barrier.
var accountProtections = map[string]func(context.Context, store.RealmStore) error{
	"operator block": func(ctx context.Context, realm store.RealmStore) error {
		return realm.SetAccountBlocked(ctx, "acc", true, "risk")
	},
	"engine block": func(ctx context.Context, realm store.RealmStore) error {
		return realm.SetAccountBlock(ctx, domain.AccountBlock{
			Account: "acc", Policy: "SpotFundsPolicy",
			Code: domain.RejectCodePnlKillSwitchTriggered, Reason: "kill switch",
		})
	},
	"blocked group": func(ctx context.Context, realm store.RealmStore) error {
		return realm.SetGroupBlocked(ctx, "desk", true, "risk")
	},
	"group-scoped spot funds pnl bound": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeAccountGroup, AccountGroup: "desk",
			Currency: "USD", UpperBound: "10",
		})
	},
	"own rate barrier": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutRateLimit(ctx, rateLimit(
			domain.ScopeAccount, "acc", "", 100, time.Second,
		))
	},
	"own order-size barrier": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
			Scope: domain.ScopeAccountSettlementAsset, Account: "acc",
			Asset: "USD", MaxNotional: "100",
		})
	},
	"own spot funds pnl bound": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeAccount, Account: "acc",
			Currency: "USD", UpperBound: "10",
		})
	},
}

// assetProtections reference asset AAPL of newSubjectNode from a barrier in
// each way an agent may not shed by deleting the asset or changing its code,
// since an asset recreated under the code is referenced by no barrier.
var assetProtections = map[string]func(context.Context, store.RealmStore) error{
	"asset rate barrier": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutRateLimit(ctx, rateLimit(
			domain.ScopeAsset, "", "AAPL", 100, time.Second,
		))
	},
	"account-asset rate barrier": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutRateLimit(ctx, rateLimit(
			domain.ScopeAccountAsset, "acc", "AAPL", 100, time.Second,
		))
	},
	"underlying-asset order-size barrier": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
			Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "1",
		})
	},
	"account-settlement-asset order-size barrier": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
			Scope: domain.ScopeAccountSettlementAsset, Account: "acc",
			Asset: "AAPL", MaxNotional: "100",
		})
	},
	"spot funds pnl bound in the asset": func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeGlobal, Currency: "AAPL", LowerBound: "-1",
		})
	},
}

// storedRiskState reads every account, group, asset and balance and every
// barrier from the node's store, each sorted by its key fields as the SQLite
// store orders it, so that a comparison does not depend on the order the
// memory realm returns its map-held rows in.
func storedRiskState(t *testing.T, n *localNode) []any {
	t.Helper()
	ctx := context.Background()
	accounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	groups, err := n.realm.ListGroups(ctx)
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	assets, err := n.realm.ListAssets(ctx)
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	balances, err := n.realm.ListBalances(ctx, "", "")
	if err != nil {
		t.Fatalf("ListBalances: %v", err)
	}
	limits, err := n.listLimits(ctx, "")
	if err != nil {
		t.Fatalf("listLimits: %v", err)
	}
	slices.SortFunc(accounts, func(a, b domain.Account) int {
		return cmp.Compare(a.Code, b.Code)
	})
	slices.SortFunc(groups, func(a, b domain.AccountGroup) int {
		return cmp.Compare(a.Code, b.Code)
	})
	slices.SortFunc(assets, func(a, b domain.Asset) int {
		return cmp.Compare(a.Code, b.Code)
	})
	slices.SortFunc(balances, func(a, b domain.Balance) int {
		return cmp.Or(cmp.Compare(a.Account, b.Account), cmp.Compare(a.Asset, b.Asset))
	})
	slices.SortFunc(limits.RateLimits, func(a, b domain.LimitRate) int {
		return cmp.Or(
			cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Account, b.Account),
			cmp.Compare(a.Asset, b.Asset),
		)
	})
	slices.SortFunc(limits.OrderSizeLimits, func(a, b domain.LimitOrderSize) int {
		return cmp.Or(
			cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Account, b.Account),
			cmp.Compare(a.Asset, b.Asset),
		)
	})
	slices.SortFunc(limits.SpotFundsPnlBoundsLimits,
		func(a, b domain.LimitSpotFundsPnlBounds) int {
			return cmp.Or(
				cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Account, b.Account),
				cmp.Compare(a.AccountGroup, b.AccountGroup),
			)
		},
	)
	return []any{accounts, groups, assets, balances, limits}
}

// refusalKeepsRiskState extends refusalLeavesNoTrace with the stored risk state
// storedRiskState reads, and returns a check that fails the test when a
// refused call moved any of it.
func refusalKeepsRiskState(
	t *testing.T, n *localNode, probe *rebuildProbe,
) func() {
	t.Helper()
	unchanged := refusalLeavesNoTrace(t, n, probe)
	before := storedRiskState(t, n)
	return func() {
		t.Helper()
		unchanged()
		if after := storedRiskState(t, n); !reflect.DeepEqual(after, before) {
			t.Fatalf(
				"refusal changed the stored risk state: before=%+v after=%+v",
				before, after,
			)
		}
	}
}

// rebuiltWithAccount reports whether the snapshot the engine was last built
// from holds account or a barrier of its own.
func rebuiltWithAccount(probe *rebuildProbe, account domain.AccountID) bool {
	last := probe.last
	return slices.ContainsFunc(last.Accounts, func(row domain.Account) bool {
		return row.Code == account
	}) || slices.ContainsFunc(last.RateLimits, func(row domain.LimitRate) bool {
		return row.Account == account
	}) || slices.ContainsFunc(last.OrderSizeLimits, func(row domain.LimitOrderSize) bool {
		return row.Account == account
	}) || slices.ContainsFunc(last.SpotFundsPnlBoundsLimits,
		func(row domain.LimitSpotFundsPnlBounds) bool { return row.Account == account },
	)
}

// rebuiltWithAsset reports whether the snapshot the engine was last built from
// holds asset or a barrier referencing it.
func rebuiltWithAsset(probe *rebuildProbe, asset string) bool {
	last := probe.last
	return slices.ContainsFunc(last.Assets, func(row domain.Asset) bool {
		return row.Code == asset
	}) || slices.ContainsFunc(last.RateLimits, func(row domain.LimitRate) bool {
		return row.Asset == asset
	}) || slices.ContainsFunc(last.OrderSizeLimits, func(row domain.LimitOrderSize) bool {
		return row.Asset == asset
	}) || slices.ContainsFunc(last.SpotFundsPnlBoundsLimits,
		func(row domain.LimitSpotFundsPnlBounds) bool { return row.Currency == asset },
	)
}

// TestLocalNode_AgentAccountReleaseRefusedWhileProtected refuses an agent's
// delete of a protected account, with or without force, and a change of its
// code, for each protection, and changes nothing; a change of its title alone
// still passes. Every other caller renames the account and deletes it with
// force as before, and the engine is rebuilt without it and its barriers.
func TestLocalNode_AgentAccountReleaseRefusedWhileProtected(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, protect := range accountProtections {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			t.Run("agent", func(t *testing.T) {
				t.Parallel()
				n, probe := newSubjectNode(t, protect)
				unchanged := refusalKeepsRiskState(t, n, probe)
				for _, force := range []bool{false, true} {
					requireLoosening(t, fmt.Sprintf("DeleteAccount(force=%v)", force),
						n.DeleteAccount(ctx, "acc", force, agentCaller))
				}
				_, err := n.UpdateAccount(
					ctx, "acc", domain.Account{Code: "acc-2"}, agentCaller,
				)
				requireLoosening(t, "UpdateAccount(acc -> acc-2)", err)
				unchanged()
				updated, err := n.UpdateAccount(
					ctx, "acc", domain.Account{Code: "acc", Title: "Desk"}, agentCaller,
				)
				if err != nil || updated.Title != "Desk" {
					t.Fatalf(
						"UpdateAccount(title) = %+v err=%v, want the title changed",
						updated, err,
					)
				}
			})
			for callerName, caller := range operatorCallers {
				t.Run(callerName, func(t *testing.T) {
					t.Parallel()
					n, probe := newSubjectNode(t, protect)
					if _, err := n.UpdateAccount(
						ctx, "acc", domain.Account{Code: "acc-2"}, caller,
					); err != nil {
						t.Fatalf("UpdateAccount(acc -> acc-2): %v", err)
					}
					if err := n.DeleteAccount(ctx, "acc-2", true, caller); err != nil {
						t.Fatalf("DeleteAccount(acc-2, force): %v", err)
					}
					if rebuiltWithAccount(probe, "acc-2") {
						t.Fatalf("engine rebuilt with acc-2 or its barriers: %+v", probe.last)
					}
				})
			}
		})
	}
}

// TestLocalNode_AgentAssetReleaseRefusedWhileBarrierReferencesIt refuses an
// agent's delete of an asset a barrier references, with or without force, and
// a change of its code, for each such barrier, and changes nothing; a change of
// its title and asset class alone still passes. Every other caller renames the
// asset and deletes it with force as before, and the engine is rebuilt without
// it and its barriers.
func TestLocalNode_AgentAssetReleaseRefusedWhileBarrierReferencesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, protect := range assetProtections {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			t.Run("agent", func(t *testing.T) {
				t.Parallel()
				n, probe := newSubjectNode(t, protect)
				unchanged := refusalKeepsRiskState(t, n, probe)
				for _, force := range []bool{false, true} {
					requireLoosening(t, fmt.Sprintf("DeleteAsset(force=%v)", force),
						n.DeleteAsset(ctx, "AAPL", force, agentCaller))
				}
				_, err := n.UpdateAsset(
					ctx, "AAPL", domain.Asset{Code: "AAPL2"}, agentCaller,
				)
				requireLoosening(t, "UpdateAsset(AAPL -> AAPL2)", err)
				unchanged()
				kept := domain.Asset{Code: "AAPL", Title: "Apple", AssetClass: "equity"}
				updated, err := n.UpdateAsset(ctx, "AAPL", kept, agentCaller)
				if err != nil || updated.Title != kept.Title ||
					updated.AssetClass != kept.AssetClass {
					t.Fatalf(
						"UpdateAsset(title, class) = %+v err=%v, want %+v",
						updated, err, kept,
					)
				}
			})
			for callerName, caller := range operatorCallers {
				t.Run(callerName, func(t *testing.T) {
					t.Parallel()
					n, probe := newSubjectNode(t, protect)
					if _, err := n.UpdateAsset(
						ctx, "AAPL", domain.Asset{Code: "AAPL2"}, caller,
					); err != nil {
						t.Fatalf("UpdateAsset(AAPL -> AAPL2): %v", err)
					}
					if err := n.DeleteAsset(ctx, "AAPL2", true, caller); err != nil {
						t.Fatalf("DeleteAsset(AAPL2, force): %v", err)
					}
					if rebuiltWithAsset(probe, "AAPL2") {
						t.Fatalf("engine rebuilt with AAPL2 or its barriers: %+v", probe.last)
					}
				})
			}
		})
	}
}

// TestLocalNode_AgentForcedAssetDeleteRefused refuses an agent's forced delete
// of asset AAPL whatever the asset carries, and changes nothing: force would
// delete the asset's dependent rows with it, here account acc's balance in the
// asset with a negative held amount, which the spendable funds subtract, or no
// row at all. No barrier references the asset and no rate-limit barrier is
// configured, so no other refusal applies. Every other caller's forced delete
// still deletes the asset and its balance, and the engine is rebuilt without
// them.
func TestLocalNode_AgentForcedAssetDeleteRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, balances := range map[string][]domain.Balance{
		"with a balance": {{
			Account: "acc", Asset: "AAPL", Available: "10", Held: "-5",
		}},
		"without dependents": nil,
	} {
		// newNode builds newSubjectNode's node holding balances.
		newNode := func(t *testing.T) (*localNode, *rebuildProbe) {
			t.Helper()
			n, probe := newSubjectNode(t)
			for _, balance := range balances {
				if err := n.realm.UpsertBalance(ctx, balance); err != nil {
					t.Fatalf("UpsertBalance: %v", err)
				}
			}
			return n, probe
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			t.Run("agent", func(t *testing.T) {
				t.Parallel()
				n, probe := newNode(t)
				unchanged := refusalKeepsRiskState(t, n, probe)
				requireLoosening(t, "DeleteAsset(AAPL, force)",
					n.DeleteAsset(ctx, "AAPL", true, agentCaller))
				unchanged()
				stored, err := n.realm.ListBalances(ctx, "", "AAPL")
				if err != nil || len(stored) != len(balances) {
					t.Fatalf(
						"AAPL balances after the refusal = %+v err=%v, want %+v",
						stored, err, balances,
					)
				}
			})
			for callerName, caller := range operatorCallers {
				t.Run(callerName, func(t *testing.T) {
					t.Parallel()
					n, probe := newNode(t)
					if err := n.DeleteAsset(ctx, "AAPL", true, caller); err != nil {
						t.Fatalf("DeleteAsset(AAPL, force): %v", err)
					}
					if _, ok, err := n.realm.GetAsset(ctx, "AAPL"); err != nil || ok {
						t.Fatalf("asset AAPL after the delete: ok=%v err=%v, want it gone", ok, err)
					}
					stored, err := n.realm.ListBalances(ctx, "", "AAPL")
					if err != nil || len(stored) != 0 {
						t.Fatalf("AAPL balances after the delete = %+v err=%v, want none", stored, err)
					}
					if rebuiltWithAsset(probe, "AAPL") || slices.ContainsFunc(
						probe.last.Balances,
						func(row domain.Balance) bool { return row.Asset == "AAPL" },
					) {
						t.Fatalf("engine rebuilt with AAPL or its balance: %+v", probe.last)
					}
				})
			}
		})
	}
}

// TestLocalNode_AgentReleasesUnprotectedSubjects lets an agent rename and then
// delete account acc and asset AAPL, which nothing protects, while account
// other is blocked and carries barriers, its group halted is blocked and
// carries a group-scoped Spot Funds P&L bound, and barriers reference asset
// USD: each guard keys on its own subject. No rate-limit barrier is
// configured, so a delete may rebuild the engine. The agent's forced delete of
// asset MSFT, which nothing protects either, is still refused, as an agent's
// forced asset delete always is.
func TestLocalNode_AgentReleasesUnprotectedSubjects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	n, _ := newSubjectNode(t)
	must := func(what string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
	}
	_, err := n.CreateGroup(ctx, domain.AccountGroup{Code: "halted"}, testCaller)
	must("CreateGroup(halted)", err)
	_, err = n.CreateAccount(ctx, domain.Account{
		Code: "other", GroupCode: "halted",
	}, testCaller)
	must("CreateAccount(other)", err)
	_, err = n.CreateAsset(ctx, domain.Asset{Code: "MSFT"}, testCaller)
	must("CreateAsset(MSFT)", err)
	must("block other", n.realm.SetAccountBlocked(ctx, "other", true, "risk"))
	must("block halted", n.realm.SetGroupBlocked(ctx, "halted", true, "risk"))
	must("bound halted", n.realm.PutSpotFundsPnlBoundsLimit(ctx,
		domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeAccountGroup, AccountGroup: "halted",
			Currency: "USD", UpperBound: "10",
		}))
	must("bound other", n.realm.PutSpotFundsPnlBoundsLimit(ctx,
		domain.LimitSpotFundsPnlBounds{
			Scope: domain.ScopeAccount, Account: "other",
			Currency: "USD", UpperBound: "10",
		}))
	must("cap other", n.realm.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
		Scope: domain.ScopeAccountSettlementAsset, Account: "other",
		Asset: "USD", MaxNotional: "100",
	}))

	_, err = n.UpdateAccount(ctx, "acc", domain.Account{Code: "acc-2"}, agentCaller)
	must("UpdateAccount(acc -> acc-2)", err)
	must("DeleteAccount(acc-2)", n.DeleteAccount(ctx, "acc-2", false, agentCaller))
	_, err = n.UpdateAsset(ctx, "AAPL", domain.Asset{Code: "AAPL2"}, agentCaller)
	must("UpdateAsset(AAPL -> AAPL2)", err)
	must("DeleteAsset(AAPL2)", n.DeleteAsset(ctx, "AAPL2", false, agentCaller))
	requireLoosening(t, "DeleteAsset(MSFT, force)",
		n.DeleteAsset(ctx, "MSFT", true, agentCaller))
}

// TestLocalNode_AgentRebuildingDeletesRefusedWhileRateBarrierExists refuses an
// agent's delete of an unprotected account, with or without force, while any
// rate-limit barrier is configured, here a broker one that references neither
// the account nor asset AAPL, because the engine rebuild would reset every rate
// window; its forced delete of the unprotected asset is refused too, as an
// agent's forced asset delete always is. The agent still renames both and
// deletes the asset without force, neither of which rebuilds the engine, and
// every other caller still deletes both with force.
func TestLocalNode_AgentRebuildingDeletesRefusedWhileRateBarrierExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	brokerRate := func(ctx context.Context, realm store.RealmStore) error {
		return realm.PutRateLimit(ctx, rateLimit(
			domain.ScopeBroker, "", "", 100, time.Second,
		))
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newSubjectNode(t, brokerRate)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for _, force := range []bool{false, true} {
			requireLoosening(t, fmt.Sprintf("DeleteAccount(force=%v)", force),
				n.DeleteAccount(ctx, "acc", force, agentCaller))
		}
		requireLoosening(t, "DeleteAsset(force=true)",
			n.DeleteAsset(ctx, "AAPL", true, agentCaller))
		unchanged()
		if _, err := n.UpdateAccount(
			ctx, "acc", domain.Account{Code: "acc-2"}, agentCaller,
		); err != nil {
			t.Fatalf("UpdateAccount(acc -> acc-2): %v", err)
		}
		if _, err := n.UpdateAsset(
			ctx, "AAPL", domain.Asset{Code: "AAPL2"}, agentCaller,
		); err != nil {
			t.Fatalf("UpdateAsset(AAPL -> AAPL2): %v", err)
		}
		if err := n.DeleteAsset(ctx, "AAPL2", false, agentCaller); err != nil {
			t.Fatalf("DeleteAsset(AAPL2): %v", err)
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newSubjectNode(t, brokerRate)
			if err := n.DeleteAccount(ctx, "acc", true, caller); err != nil {
				t.Fatalf("DeleteAccount(acc, force): %v", err)
			}
			prepareGroupDeleteRebuild(n, probe, newResolvingEngine())
			if err := n.DeleteAsset(ctx, "AAPL", true, caller); err != nil {
				t.Fatalf("DeleteAsset(AAPL, force): %v", err)
			}
		})
	}
}

// TestLocalNode_AgentReleaseFailsOnUnreadableState answers an agent's release
// of an unprotected account or asset with the failure of a read the decision
// needs, instead of deciding without it, and releases nothing: a barrier read,
// the read of the account's group, or the account's group missing from the
// store. Only that read fails, so a decision that went on without it would
// release the subject. An agent's forced asset delete reads nothing: it is
// refused outright.
func TestLocalNode_AgentReleaseFailsOnUnreadableState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	readErr := errors.New("read failure")
	accountReleases := map[string]func(*localNode) error{
		"DeleteAccount": func(n *localNode) error {
			return n.DeleteAccount(ctx, "acc", false, agentCaller)
		},
		"UpdateAccount": func(n *localNode) error {
			_, err := n.UpdateAccount(
				ctx, "acc", domain.Account{Code: "acc-2"}, agentCaller,
			)
			return err
		},
	}
	releases := map[string]func(*localNode) error{
		"DeleteAsset": func(n *localNode) error {
			return n.DeleteAsset(ctx, "AAPL", false, agentCaller)
		},
		"UpdateAsset": func(n *localNode) error {
			_, err := n.UpdateAsset(
				ctx, "AAPL", domain.Asset{Code: "AAPL2"}, agentCaller,
			)
			return err
		},
	}
	for name, release := range accountReleases {
		releases[name] = release
	}

	for name, release := range releases {
		for read, realm := range map[string]failingBarrierReadsRealm{
			"rate barriers":         {rateErr: readErr},
			"spot funds pnl bounds": {boundsErr: readErr},
		} {
			t.Run(name+"/"+read, func(t *testing.T) {
				t.Parallel()
				n, probe := newSubjectNode(t)
				unchanged := refusalKeepsRiskState(t, n, probe)
				realm.RealmStore = n.realm
				n.realm = &realm
				if err := release(n); !errors.Is(err, readErr) {
					t.Fatalf("%s = %v, want the read failure", name, err)
				}
				unchanged()
			})
		}
	}
	for name, release := range accountReleases {
		t.Run(name+"/group", func(t *testing.T) {
			t.Parallel()
			n, probe := newSubjectNode(t)
			unchanged := refusalKeepsRiskState(t, n, probe)
			n.realm = &failSelectedGroupRereadsRealm{
				RealmStore: n.realm, failCalls: map[int]error{1: readErr},
			}
			if err := release(n); !errors.Is(err, readErr) {
				t.Fatalf("%s = %v, want the read failure", name, err)
			}
			unchanged()
		})
		t.Run(name+"/unstored group", func(t *testing.T) {
			t.Parallel()
			n, probe := newSubjectNode(t)
			realm, ok := n.realm.(*memoryRealm)
			if !ok {
				t.Fatalf("node realm is %T, want *memoryRealm", n.realm)
			}
			delete(realm.groups, "desk")
			unchanged := refusalKeepsRiskState(t, n, probe)
			if err := release(n); err == nil || errors.Is(err, domain.ErrLoosening) {
				t.Fatalf("%s = %v, want a failure naming the unstored group", name, err)
			}
			unchanged()
		})
	}
}

// newLimitNode builds a node on a fake engine whose builds probe counts,
// holding the assets USD, EUR and AAPL and the barriers of stored, put
// straight into its store.
func newLimitNode(t *testing.T, stored AccountLimits) (*localNode, *rebuildProbe) {
	t.Helper()
	ctx := context.Background()
	n, probe := newRebuildProbeNode(t, newFakeEngine())
	createCurrencyAssets(t, n.realm, "USD", "EUR", "AAPL")
	for _, limit := range stored.RateLimits {
		if err := n.realm.PutRateLimit(ctx, limit); err != nil {
			t.Fatalf("PutRateLimit: %v", err)
		}
	}
	for _, limit := range stored.OrderSizeLimits {
		if err := n.realm.PutOrderSizeLimit(ctx, limit); err != nil {
			t.Fatalf("PutOrderSizeLimit: %v", err)
		}
	}
	for _, limit := range stored.SpotFundsPnlBoundsLimits {
		if err := n.realm.PutSpotFundsPnlBoundsLimit(ctx, limit); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
	}
	return n, probe
}

// requireStored fails the test unless the node's store holds want, a barrier
// of any kind, exactly.
func requireStored(t *testing.T, n *localNode, want any) {
	t.Helper()
	limits, err := n.listLimits(context.Background(), "")
	if err != nil {
		t.Fatalf("listLimits: %v", err)
	}
	var stored bool
	switch want := want.(type) {
	case domain.LimitRate:
		stored = slices.Contains(limits.RateLimits, want)
	case domain.LimitOrderSize:
		stored = slices.Contains(limits.OrderSizeLimits, want)
	case domain.LimitSpotFundsPnlBounds:
		stored = slices.Contains(limits.SpotFundsPnlBoundsLimits, want)
	default:
		t.Fatalf("requireStored(%T): not a barrier", want)
	}
	if !stored {
		t.Fatalf("stored barriers = %+v, want %+v among them", limits, want)
	}
}

// pnlBound builds a Spot Funds P&L bound in currency at scope, addressed to
// subject: the account at the account scope, the group at the account_group
// scope, nothing at the global one.
func pnlBound(
	scope domain.LimitScope, subject, currency, lower, upper string,
) domain.LimitSpotFundsPnlBounds {
	bound := domain.LimitSpotFundsPnlBounds{
		Scope: scope, Currency: currency, LowerBound: lower, UpperBound: upper,
	}
	switch scope {
	case domain.ScopeAccount:
		bound.Account = domain.AccountID(subject)
	case domain.ScopeAccountGroup:
		bound.AccountGroup = subject
	}
	return bound
}

// TestLocalNode_AgentRateLimitTightensOnly refuses an agent's broker rate
// barrier with more orders, or over a window of another length, than the
// barrier it would replace and changes nothing, and stores its barrier with
// fewer orders over the same window exactly as sent. Every matching rate
// barrier applies, so a barrier under a new key only adds a limit: the agent
// adds one with more orders than the broker barrier, for an account Officer
// knows. Every other caller still loosens the broker barrier.
func TestLocalNode_AgentRateLimitTightensOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	newNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		return newLimitNode(t, AccountLimits{RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		}})
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newNode(t)
		if _, err := n.CreateAccount(ctx, domain.Account{Code: "acc"}, testCaller); err != nil {
			t.Fatalf("CreateAccount(acc): %v", err)
		}
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, looser := range map[string]domain.LimitRate{
			"more orders": rateLimit(domain.ScopeBroker, "", "", 101, time.Second),
			"shorter window": rateLimit(
				domain.ScopeBroker, "", "", 100, time.Second-time.Millisecond,
			),
			"longer window": rateLimit(domain.ScopeBroker, "", "", 100, 2*time.Second),
		} {
			_, err := n.PutRateLimit(ctx, looser, domain.MissingAccountCreate, agentCaller)
			requireLoosening(t, "PutRateLimit("+name+")", err)
		}
		unchanged()
		for _, accepted := range []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 50, time.Second),
			rateLimit(domain.ScopeAccount, "acc", "", 1000, time.Second),
		} {
			if _, err := n.PutRateLimit(
				ctx, accepted, domain.MissingAccountCreate, agentCaller,
			); err != nil {
				t.Fatalf("PutRateLimit(%+v): %v", accepted, err)
			}
			requireStored(t, n, accepted)
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newNode(t)
			looser := rateLimit(domain.ScopeBroker, "", "", 1000, time.Millisecond)
			if _, err := n.PutRateLimit(
				ctx, looser, domain.MissingAccountCreate, caller,
			); err != nil {
				t.Fatalf("PutRateLimit: %v", err)
			}
			requireStored(t, n, looser)
		})
	}
}

// TestLocalNode_AgentOrderSizeLimitTightensOnly refuses an agent's order-size
// barrier with a cap above, or without a cap of, the barrier it would replace
// and changes nothing, and stores its barrier with no cap above it exactly as
// sent. Every other caller still loosens the barrier.
func TestLocalNode_AgentOrderSizeLimitTightensOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := func(quantity, notional string) domain.LimitOrderSize {
		return domain.LimitOrderSize{
			Scope: domain.ScopeBroker, MaxQuantity: quantity, MaxNotional: notional,
		}
	}
	newNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		return newLimitNode(t, AccountLimits{
			OrderSizeLimits: []domain.LimitOrderSize{broker("100", "1000")},
		})
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newNode(t)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, looser := range map[string]domain.LimitOrderSize{
			"higher quantity":  broker("100.5", "1000"),
			"dropped notional": broker("100", ""),
		} {
			_, err := n.PutOrderSizeLimit(
				ctx, looser, domain.MissingAccountCreate, agentCaller,
			)
			requireLoosening(t, "PutOrderSizeLimit("+name+")", err)
		}
		unchanged()
		stricter := broker("50", "1000.0")
		if _, err := n.PutOrderSizeLimit(
			ctx, stricter, domain.MissingAccountCreate, agentCaller,
		); err != nil {
			t.Fatalf("PutOrderSizeLimit: %v", err)
		}
		requireStored(t, n, stricter)
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newNode(t)
			looser := broker("1000", "")
			if _, err := n.PutOrderSizeLimit(
				ctx, looser, domain.MissingAccountCreate, caller,
			); err != nil {
				t.Fatalf("PutOrderSizeLimit: %v", err)
			}
			requireStored(t, n, looser)
		})
	}
}

// TestLocalNode_AgentAccountOrderSizeCapTightensAssetCap refuses an agent's
// account-tier order-size cap above the asset-tier cap it would override on
// the same asset - account_underlying_asset over underlying_asset,
// account_settlement_asset over settlement_asset - and changes nothing. It
// stores the agent's stricter caps exactly as sent. Every other caller still
// sets an account-tier cap above the asset-tier one.
func TestLocalNode_AgentAccountOrderSizeCapTightensAssetCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	underlying := func(quantity string) domain.LimitOrderSize {
		return domain.LimitOrderSize{
			Scope: domain.ScopeAccountUnderlyingAsset, Account: "acc",
			Asset: "AAPL", MaxQuantity: quantity,
		}
	}
	settlement := func(notional string) domain.LimitOrderSize {
		return domain.LimitOrderSize{
			Scope: domain.ScopeAccountSettlementAsset, Account: "acc",
			Asset: "USD", MaxNotional: notional,
		}
	}
	newNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		n, probe := newLimitNode(t, AccountLimits{OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeUnderlyingAsset, Asset: "AAPL", MaxQuantity: "100"},
			{Scope: domain.ScopeSettlementAsset, Asset: "USD", MaxNotional: "1000"},
		}})
		if _, err := n.CreateAccount(ctx, domain.Account{Code: "acc"}, testCaller); err != nil {
			t.Fatalf("CreateAccount(acc): %v", err)
		}
		return n, probe
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newNode(t)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, looser := range map[string]domain.LimitOrderSize{
			"account_underlying_asset": underlying("100.01"),
			"account_settlement_asset": settlement("1000.01"),
		} {
			_, err := n.PutOrderSizeLimit(
				ctx, looser, domain.MissingAccountCreate, agentCaller,
			)
			requireLoosening(t, "PutOrderSizeLimit("+name+")", err)
		}
		unchanged()
		for _, stricter := range []domain.LimitOrderSize{
			underlying("99"), settlement("999"),
		} {
			if _, err := n.PutOrderSizeLimit(
				ctx, stricter, domain.MissingAccountCreate, agentCaller,
			); err != nil {
				t.Fatalf("PutOrderSizeLimit(%+v): %v", stricter, err)
			}
			requireStored(t, n, stricter)
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newNode(t)
			for _, looser := range []domain.LimitOrderSize{
				underlying("1000"), settlement("10000"),
			} {
				if _, err := n.PutOrderSizeLimit(
					ctx, looser, domain.MissingAccountCreate, caller,
				); err != nil {
					t.Fatalf("PutOrderSizeLimit(%+v): %v", looser, err)
				}
				requireStored(t, n, looser)
			}
		})
	}
}

// TestLocalNode_AgentFirstOrderSizeLimitRefusedWhileRateBarrierExists refuses
// an agent's first order-size barrier while a rate-limit barrier is
// configured, because the engine rebuild that installs the policy's first
// barrier would reset every rate window, and changes nothing: the asset it
// names is not created and the engine is not rebuilt. Every other caller
// installs it, and so does the agent while no rate-limit barrier is
// configured.
func TestLocalNode_AgentFirstOrderSizeLimitRefusedWhileRateBarrierExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := domain.LimitOrderSize{
		Scope: domain.ScopeUnderlyingAsset, Asset: "MSFT", MaxQuantity: "10",
	}
	rateBarrier := AccountLimits{RateLimits: []domain.LimitRate{
		rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
	}}
	// requireInstalled installs first as caller and fails the test unless the
	// install rebuilt the engine and stored it.
	requireInstalled := func(t *testing.T, stored AccountLimits, caller domain.Caller) {
		t.Helper()
		n, probe := newLimitNode(t, stored)
		prepareGroupDeleteRebuild(n, probe, newFakeEngine())
		builds := probe.builds
		if _, err := n.PutOrderSizeLimit(
			ctx, first, domain.MissingAccountCreate, caller,
		); err != nil {
			t.Fatalf("PutOrderSizeLimit: %v", err)
		}
		if probe.builds != builds+1 {
			t.Fatalf("engine builds %d -> %d, want one rebuild", builds, probe.builds)
		}
		requireStored(t, n, first)
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newLimitNode(t, rateBarrier)
		prepareGroupDeleteRebuild(n, probe, newFakeEngine())
		unchanged := refusalKeepsRiskState(t, n, probe)
		_, err := n.PutOrderSizeLimit(ctx, first, domain.MissingAccountCreate, agentCaller)
		requireLoosening(t, "PutOrderSizeLimit", err)
		unchanged()
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			requireInstalled(t, rateBarrier, caller)
		})
	}
	t.Run("agent without a rate barrier", func(t *testing.T) {
		t.Parallel()
		requireInstalled(t, AccountLimits{}, agentCaller)
	})
}

// TestLocalNode_AgentSpotFundsPnlBoundsTightensOnly refuses an agent's Spot
// Funds P&L bound with its lower bound below or its upper bound above the
// bound it would replace, without one of its bounds, or in another currency,
// and changes nothing, and stores its stricter bound exactly as sent. Every
// other caller still loosens the bound.
func TestLocalNode_AgentSpotFundsPnlBoundsTightensOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	global := func(currency, lower, upper string) domain.LimitSpotFundsPnlBounds {
		return pnlBound(domain.ScopeGlobal, "", currency, lower, upper)
	}
	newNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		return newLimitNode(t, AccountLimits{
			SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
				global("USD", "-100", "100"),
			},
		})
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newNode(t)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, looser := range map[string]domain.LimitSpotFundsPnlBounds{
			"lower bound lowered": global("USD", "-100.5", "100"),
			"upper bound raised":  global("USD", "-100", "100.5"),
			"upper bound dropped": global("USD", "-100", ""),
			"another currency":    global("EUR", "-100", "100"),
		} {
			_, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, looser, domain.MissingAccountCreate, agentCaller,
			)
			requireLoosening(t, "PutSpotFundsPnlBoundsLimit("+name+")", err)
		}
		unchanged()
		stricter := global("USD", "-50", "50.0")
		if _, err := n.PutSpotFundsPnlBoundsLimit(
			ctx, stricter, domain.MissingAccountCreate, agentCaller,
		); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
		requireStored(t, n, stricter)
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newNode(t)
			looser := global("USD", "-1000", "")
			if _, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, looser, domain.MissingAccountCreate, caller,
			); err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
			}
			requireStored(t, n, looser)
		})
	}
}

// TestLocalNode_AgentAccountPnlBoundTightensResolvedTier compares an agent's
// first account-tier Spot Funds P&L bound with the bound the cascade resolves
// for the account now. Member, in group desk, gets desk's bound in USD, and so
// does dollar, in desk with its own currency USD. Solo, in no group, gets the
// global bound in USD, and so do crossed, with its own currency USD in group
// fx whose bound is in EUR, and pooled, in group pool without a bound. Euro,
// in desk but with its own currency EUR, gets neither, since both are in USD.
// A bound looser than the resolved one is refused against it and changes
// nothing, a stricter one is stored exactly as sent, and euro takes any bound.
// Every other caller still sets member a bound looser than its group's.
func TestLocalNode_AgentAccountPnlBoundTightensResolvedTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	account := func(code, currency, lower, upper string) domain.LimitSpotFundsPnlBounds {
		return pnlBound(domain.ScopeAccount, code, currency, lower, upper)
	}
	newNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		n, probe := newLimitNode(t, AccountLimits{})
		for _, code := range []string{"desk", "fx", "pool"} {
			if _, err := n.CreateGroup(
				ctx, domain.AccountGroup{Code: code}, testCaller,
			); err != nil {
				t.Fatalf("CreateGroup(%s): %v", code, err)
			}
		}
		for _, created := range []domain.Account{
			{Code: "member", GroupCode: "desk"},
			{Code: "solo"},
			{Code: "euro", GroupCode: "desk", Currency: "EUR"},
			{Code: "dollar", GroupCode: "desk", Currency: "USD"},
			{Code: "crossed", GroupCode: "fx", Currency: "USD"},
			{Code: "pooled", GroupCode: "pool"},
		} {
			if _, err := n.CreateAccount(ctx, created, testCaller); err != nil {
				t.Fatalf("CreateAccount(%s): %v", created.Code, err)
			}
		}
		for _, bound := range []domain.LimitSpotFundsPnlBounds{
			pnlBound(domain.ScopeAccountGroup, "desk", "USD", "-100", "100"),
			pnlBound(domain.ScopeAccountGroup, "fx", "EUR", "-10", "10"),
			pnlBound(domain.ScopeGlobal, "", "USD", "-1000", "1000"),
		} {
			if err := n.realm.PutSpotFundsPnlBoundsLimit(ctx, bound); err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
			}
		}
		return n, probe
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newNode(t)
		unchanged := refusalKeepsRiskState(t, n, probe)
		// against is the lower bound of the resolved bound, which the
		// refusal names.
		for name, tc := range map[string]struct {
			looser  domain.LimitSpotFundsPnlBounds
			against string
		}{
			"member below its group's bound": {account("member", "USD", "-200", "100"), "-100"},
			"dollar below its group's bound": {account("dollar", "USD", "-200", "100"), "-100"},
			"solo below the global bound":    {account("solo", "USD", "-2000", "1000"), "-1000"},
			"crossed below the global bound": {account("crossed", "USD", "-2000", "1000"), "-1000"},
			"pooled below the global bound":  {account("pooled", "USD", "-2000", "1000"), "-1000"},
		} {
			_, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, tc.looser, domain.MissingAccountCreate, agentCaller,
			)
			requireLoosening(t, "PutSpotFundsPnlBoundsLimit("+name+")", err)
			if want := "below the current " + tc.against + ":"; !strings.Contains(err.Error(), want) {
				t.Fatalf("PutSpotFundsPnlBoundsLimit(%s) = %v, want it %s", name, err, want)
			}
		}
		unchanged()
		for _, accepted := range []domain.LimitSpotFundsPnlBounds{
			account("member", "USD", "-50", "100"),
			account("dollar", "USD", "-50", "100"),
			account("solo", "USD", "-500", "500"),
			account("crossed", "USD", "-500", "500"),
			account("pooled", "USD", "-500", "500"),
			account("euro", "EUR", "-5000", ""),
		} {
			if _, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, accepted, domain.MissingAccountCreate, agentCaller,
			); err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit(%+v): %v", accepted, err)
			}
			requireStored(t, n, accepted)
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newNode(t)
			looser := account("member", "USD", "-10000", "")
			if _, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, looser, domain.MissingAccountCreate, caller,
			); err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
			}
			requireStored(t, n, looser)
		})
	}
}

// TestLocalNode_AgentGroupPnlBoundTightensGlobal compares an agent's first
// group-tier Spot Funds P&L bound with the global bound: one in another
// currency or looser is refused and changes nothing, and one in the global
// currency and stricter is stored exactly as sent. Every other caller still
// sets a group bound in another currency.
func TestLocalNode_AgentGroupPnlBoundTightensGlobal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	desk := func(currency, lower, upper string) domain.LimitSpotFundsPnlBounds {
		return pnlBound(domain.ScopeAccountGroup, "desk", currency, lower, upper)
	}
	newNode := func(t *testing.T) (*localNode, *rebuildProbe) {
		t.Helper()
		n, probe := newLimitNode(t, AccountLimits{
			SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
				pnlBound(domain.ScopeGlobal, "", "USD", "-100", "100"),
			},
		})
		if _, err := n.CreateGroup(
			ctx, domain.AccountGroup{Code: "desk"}, testCaller,
		); err != nil {
			t.Fatalf("CreateGroup(desk): %v", err)
		}
		return n, probe
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newNode(t)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, looser := range map[string]domain.LimitSpotFundsPnlBounds{
			"another currency": desk("EUR", "-50", "50"),
			"looser":           desk("USD", "-200", "100"),
		} {
			_, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, looser, domain.MissingAccountCreate, agentCaller,
			)
			requireLoosening(t, "PutSpotFundsPnlBoundsLimit("+name+")", err)
		}
		unchanged()
		stricter := desk("USD", "-50", "50")
		if _, err := n.PutSpotFundsPnlBoundsLimit(
			ctx, stricter, domain.MissingAccountCreate, agentCaller,
		); err != nil {
			t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
		}
		requireStored(t, n, stricter)
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newNode(t)
			other := desk("EUR", "-1000", "")
			if _, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, other, domain.MissingAccountCreate, caller,
			); err != nil {
				t.Fatalf("PutSpotFundsPnlBoundsLimit: %v", err)
			}
			requireStored(t, n, other)
		})
	}
}

// TestLocalNode_AgentPnlBoundForUnknownAccount compares an agent's Spot Funds
// P&L bound for account ghost, which Officer does not know yet, with the bound
// the cascade resolves for an account in no group and with no currency of its
// own. Its effective currency is the default group's, unset here, so the
// global bound applies and refuses the looser bound. Once the default group
// currency is EUR, the global bound in USD does not apply to ghost, so the
// bound passes the comparison, and registering ghost is refused instead, as
// an agent may not create an account. Either refusal changes nothing, so
// ghost is not created.
func TestLocalNode_AgentPnlBoundForUnknownAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	bound := pnlBound(domain.ScopeAccount, "ghost", "USD", "-200", "100")
	ghostStored := func(t *testing.T, n *localNode) bool {
		t.Helper()
		_, ok, err := n.realm.GetAccount(ctx, "ghost")
		if err != nil {
			t.Fatalf("GetAccount(ghost): %v", err)
		}
		return ok
	}

	for name, tc := range map[string]struct {
		// currency is the default group's currency, none when empty.
		currency string
		// refusal is what the refusal names: the resolved bound, or ghost.
		refusal string
	}{
		"no default currency":  {"", "below the current -100:"},
		"default currency EUR": {"EUR", `a new account "ghost"`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newLimitNode(t, AccountLimits{
				SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
					pnlBound(domain.ScopeGlobal, "", "USD", "-100", "100"),
				},
			})
			if tc.currency != "" {
				if err := n.SetDefaultGroupCurrency(ctx, tc.currency, testCaller); err != nil {
					t.Fatalf("SetDefaultGroupCurrency: %v", err)
				}
			}
			unchanged := refusalKeepsRiskState(t, n, probe)
			_, err := n.PutSpotFundsPnlBoundsLimit(
				ctx, bound, domain.MissingAccountCreate, agentCaller,
			)
			requireLoosening(t, "PutSpotFundsPnlBoundsLimit", err)
			if !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("PutSpotFundsPnlBoundsLimit = %v, want it to name %s", err, tc.refusal)
			}
			unchanged()
			if ghostStored(t, n) {
				t.Fatal("account ghost was created by the refused bound")
			}
		})
	}
}

// TestLocalNode_AgentDeleteLimitRefused refuses an agent's delete of a rate,
// order-size or Spot Funds P&L barrier, since a removed barrier stops
// limiting, and changes nothing. Every other caller still deletes each.
func TestLocalNode_AgentDeleteLimitRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stored := AccountLimits{
		RateLimits: []domain.LimitRate{
			rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
		},
		OrderSizeLimits: []domain.LimitOrderSize{
			{Scope: domain.ScopeBroker, MaxQuantity: "100"},
		},
		SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
			pnlBound(domain.ScopeGlobal, "", "USD", "-100", "100"),
		},
	}
	targets := []LimitTarget{
		{Policy: domain.PolicyRateLimit, Scope: domain.ScopeBroker},
		{Policy: domain.PolicyOrderSizeLimit, Scope: domain.ScopeBroker},
		{Policy: domain.PolicySpotFundsPnlBoundsKillSwitch, Scope: domain.ScopeGlobal},
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newLimitNode(t, stored)
		// A delete that went on would rebuild the engine and succeed.
		prepareGroupDeleteRebuild(n, probe, newFakeEngine())
		unchanged := refusalKeepsRiskState(t, n, probe)
		for _, target := range targets {
			_, err := n.DeleteLimit(ctx, target, agentCaller)
			requireLoosening(t, "DeleteLimit("+target.Policy+")", err)
		}
		unchanged()
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newLimitNode(t, stored)
			for _, target := range targets {
				// Deleting a policy's last barrier rebuilds the engine.
				prepareGroupDeleteRebuild(n, probe, newFakeEngine())
				if _, err := n.DeleteLimit(ctx, target, caller); err != nil {
					t.Fatalf("DeleteLimit(%s): %v", target.Policy, err)
				}
			}
			limits, err := n.listLimits(ctx, "")
			if err != nil || len(limits.RateLimits) != 0 ||
				len(limits.OrderSizeLimits) != 0 ||
				len(limits.SpotFundsPnlBoundsLimits) != 0 {
				t.Fatalf("barriers after the deletes = %+v err=%v, want none", limits, err)
			}
		})
	}
}

// TestLocalNode_AgentLimitFailsOnUnreadableState answers an agent's barrier
// with the failure of a read its decision needs, instead of deciding without
// it, and changes nothing: the rate barriers the engine rebuild installing a
// first order-size barrier is decided on, the account whose resolved P&L bound
// an account bound is compared with, and the default group whose currency an
// account Officer does not know yet would take. Only that read fails, so any
// other answer was decided without it.
func TestLocalNode_AgentLimitFailsOnUnreadableState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	readErr := errors.New("read failure")
	for name, tc := range map[string]struct {
		stored AccountLimits
		failed func(store.RealmStore) store.RealmStore
		put    func(*localNode) error
	}{
		"first order-size barrier": {
			stored: AccountLimits{RateLimits: []domain.LimitRate{
				rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			}},
			failed: func(realm store.RealmStore) store.RealmStore {
				return &failingBarrierReadsRealm{RealmStore: realm, rateErr: readErr}
			},
			put: func(n *localNode) error {
				_, err := n.PutOrderSizeLimit(ctx, domain.LimitOrderSize{
					Scope: domain.ScopeBroker, MaxQuantity: "10",
				}, domain.MissingAccountCreate, agentCaller)
				return err
			},
		},
		"account bound": {
			stored: AccountLimits{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
				pnlBound(domain.ScopeGlobal, "", "USD", "-100", "100"),
			}},
			failed: func(realm store.RealmStore) store.RealmStore {
				return &failSelectedAccountRereadsRealm{
					RealmStore: realm, failCalls: map[int]error{1: readErr},
				}
			},
			put: func(n *localNode) error {
				_, err := n.PutSpotFundsPnlBoundsLimit(ctx, pnlBound(
					domain.ScopeAccount, "acc", "USD", "-50", "50",
				), domain.MissingAccountCreate, agentCaller)
				return err
			},
		},
		"unknown account bound": {
			stored: AccountLimits{SpotFundsPnlBoundsLimits: []domain.LimitSpotFundsPnlBounds{
				pnlBound(domain.ScopeGlobal, "", "USD", "-100", "100"),
			}},
			failed: func(realm store.RealmStore) store.RealmStore {
				return &failSelectedGroupRereadsRealm{
					RealmStore: realm, failCalls: map[int]error{1: readErr},
				}
			},
			put: func(n *localNode) error {
				_, err := n.PutSpotFundsPnlBoundsLimit(ctx, pnlBound(
					domain.ScopeAccount, "ghost", "USD", "-50", "50",
				), domain.MissingAccountCreate, agentCaller)
				return err
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newLimitNode(t, tc.stored)
			if _, err := n.CreateAccount(ctx, domain.Account{Code: "acc"}, testCaller); err != nil {
				t.Fatalf("CreateAccount(acc): %v", err)
			}
			prepareGroupDeleteRebuild(n, probe, newFakeEngine())
			unchanged := refusalKeepsRiskState(t, n, probe)
			n.realm = tc.failed(n.realm)
			if err := tc.put(n); !errors.Is(err, readErr) {
				t.Fatalf("%s = %v, want the read failure", name, err)
			}
			unchanged()
		})
	}
}

// newFundsNode builds a node over a fake engine, which it returns, holding
// account acc, whose own currency is USD - the one asset an agent may adjust -
// with balances, as an operator set them up.
func newFundsNode(
	t *testing.T, balances ...domain.Balance,
) (*localNode, *rebuildProbe, *fakeEngine) {
	t.Helper()
	ctx := context.Background()
	eng := newFakeEngine()
	n, probe := newRebuildProbeNode(t, eng)
	if _, err := n.CreateAccount(
		ctx, domain.Account{Code: "acc", Currency: "USD"}, testCaller,
	); err != nil {
		t.Fatalf("CreateAccount(acc): %v", err)
	}
	for _, balance := range balances {
		if err := n.realm.UpsertBalance(ctx, balance); err != nil {
			t.Fatalf("UpsertBalance(%s): %v", balance.Asset, err)
		}
	}
	return n, probe, eng
}

// requireNoAdjustment fails the test when the engine received an adjustment
// or the store recorded one for acc.
func requireNoAdjustment(t *testing.T, n *localNode, eng *fakeEngine) {
	t.Helper()
	if len(eng.adjustmentCalls) != 0 {
		t.Fatalf("engine adjustment calls = %+v, want none", eng.adjustmentCalls)
	}
	records, err := n.realm.ListAdjustments(context.Background(), "acc", "", 10)
	if err != nil {
		t.Fatalf("ListAdjustments(acc): %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("adjustments of acc = %+v, want none", records)
	}
}

// TestLocalNode_AgentAdjustmentOnlyLowersBalances refuses an agent's
// adjustment of the account's currency that raises balance (available), held
// or incoming - a delta above zero, or an absolute above the field's current
// value - and changes nothing: the balances stay, the engine is not called,
// and no adjustment or audit is recorded. One that raises one field and lowers
// another is refused whole. A delta of zero or below, and an absolute at or
// below the current value, reach the engine. Every other caller still raises a
// balance.
func TestLocalNode_AgentAdjustmentOnlyLowersBalances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stored := domain.Balance{
		Account: "acc", Asset: "USD", Available: "100", Held: "10", Incoming: "20",
	}
	delta := func(value string) *domain.AdjustmentAmount {
		return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: value}
	}
	absolute := func(value string) *domain.AdjustmentAmount {
		return &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: value}
	}
	adjust := func(
		asset string, balance, held, incoming *domain.AdjustmentAmount,
	) domain.AdjustmentRequest {
		return domain.AdjustmentRequest{
			Type: domain.AdjustmentTypeCorrection, Asset: asset,
			Balance: balance, Held: held, Incoming: incoming,
		}
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe, eng := newFundsNode(t, stored)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, raising := range map[string]domain.AdjustmentRequest{
			"balance delta":     adjust("USD", delta("0.01"), nil, nil),
			"balance absolute":  adjust("USD", absolute("100.01"), nil, nil),
			"held delta":        adjust("USD", nil, delta("1"), nil),
			"held absolute":     adjust("USD", nil, absolute("10.5"), nil),
			"incoming delta":    adjust("USD", nil, nil, delta("1")),
			"incoming absolute": adjust("USD", nil, nil, absolute("21")),
			"one field of two":  adjust("USD", delta("-1"), delta("1"), nil),
		} {
			_, err := n.ApplyAdjustment(
				ctx, "acc", "", raising, domain.MissingAccountReject, agentCaller,
			)
			requireLoosening(t, "ApplyAdjustment("+name+")", err)
		}
		unchanged()
		requireNoAdjustment(t, n, eng)
		lowering := map[string]domain.AdjustmentRequest{
			"deltas at or below zero": adjust(
				"USD", delta("-1"), delta("0"), delta("-20"),
			),
			"absolutes at or below the current": adjust(
				"USD", absolute("99.99"), absolute("10"), absolute("-1"),
			),
		}
		for name, request := range lowering {
			rec, err := n.ApplyAdjustment(
				ctx, "acc", "", request, domain.MissingAccountReject, agentCaller,
			)
			if err != nil || rec.Accepted == nil {
				t.Fatalf("ApplyAdjustment(%s) = %+v, %v, want it accepted", name, rec, err)
			}
		}
		if len(eng.adjustmentCalls) != len(lowering) {
			t.Fatalf(
				"engine adjustment calls = %+v, want the %d lowering ones",
				eng.adjustmentCalls, len(lowering),
			)
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _, eng := newFundsNode(t, stored)
			eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
				BalanceDelta: "50", BalanceResult: "150",
			}
			rec, err := n.ApplyAdjustment(
				ctx, "acc", "", adjust("USD", delta("50"), nil, nil),
				domain.MissingAccountReject, caller,
			)
			if err != nil || rec.Accepted == nil {
				t.Fatalf("ApplyAdjustment = %+v, %v, want it accepted", rec, err)
			}
			balance, _, err := n.realm.GetBalance(ctx, "acc", "USD")
			if err != nil || balance.Available != "150" {
				t.Fatalf("balance after the deposit = %+v err=%v, want 150 available", balance, err)
			}
		})
	}
}

// TestLocalNode_AgentRealizedPnlRefusedOnHaltedPosition refuses an agent's
// adjustment carrying a realized P&L for a position whose P&L calculation is
// halted, as it would restart it - the stored value too, which an agent may
// otherwise name - and changes nothing. Every other caller still restarts a
// halted one with the same adjustment.
func TestLocalNode_AgentRealizedPnlRefusedOnHaltedPosition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	halted := domain.Balance{
		Account: "acc", Asset: "USD", RealizedPnl: "-1",
		RealizedPnlHaltReason: domain.PnlHaltReasonMissingCostBasis,
	}
	restart := domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeCorrection, Asset: "USD", RealizedPnl: "-1",
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe, eng := newFundsNode(t, halted)
		unchanged := refusalKeepsRiskState(t, n, probe)
		_, err := n.ApplyAdjustment(
			ctx, "acc", "", restart, domain.MissingAccountReject, agentCaller,
		)
		requireLoosening(t, "ApplyAdjustment(halted)", err)
		unchanged()
		requireNoAdjustment(t, n, eng)
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _, eng := newFundsNode(t, halted)
			eng.adjustmentAccepted = &domain.AdjustmentOutcomeAccepted{
				RealizedPnlDelta: "0", RealizedPnlResult: "-1",
			}
			rec, err := n.ApplyAdjustment(
				ctx, "acc", "", restart, domain.MissingAccountReject, caller,
			)
			if err != nil || rec.Accepted == nil {
				t.Fatalf("ApplyAdjustment = %+v, %v, want it accepted", rec, err)
			}
			balance, _, err := n.realm.GetBalance(ctx, "acc", "USD")
			if err != nil || balance.RealizedPnlHaltReason != "" {
				t.Fatalf("balance after the restart = %+v err=%v, want it no longer halted", balance, err)
			}
		})
	}
}

// TestLocalNode_AgentAdjustmentOnlyOfAccountCurrency refuses an agent's
// adjustment of any asset but the account's own currency, even one that
// lowers it - a position such as AAPL, by a delta, to an absolute or in its
// held, another currency, and an asset Officer does not know yet - and any
// adjustment of an account without a currency of its own, one that inherits
// its group's included, and changes nothing: the balances stay, no asset is
// created, the engine is not called, and no adjustment or audit is recorded.
// Every other caller still applies each of them, creating the unknown asset.
func TestLocalNode_AgentAdjustmentOnlyOfAccountCurrency(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lower := func(
		asset string, balance, held *domain.AdjustmentAmount,
	) domain.AdjustmentRequest {
		return domain.AdjustmentRequest{
			Type: domain.AdjustmentTypeCorrection, Asset: asset,
			Balance: balance, Held: held,
		}
	}
	delta := &domain.AdjustmentAmount{Mode: domain.AdjustmentModeDelta, Value: "-1"}
	absolute := &domain.AdjustmentAmount{Mode: domain.AdjustmentModeAbsolute, Value: "5"}
	requests := map[string]struct {
		account domain.AccountID
		request domain.AdjustmentRequest
	}{
		"position delta":    {"acc", lower("AAPL", delta, nil)},
		"position absolute": {"acc", lower("AAPL", absolute, nil)},
		"position held":     {"acc", lower("AAPL", nil, delta)},
		"another currency":  {"acc", lower("EUR", delta, nil)},
		"unknown asset":     {"acc", lower("GOLD", delta, nil)},
		"no currency":       {"bare", lower("USD", delta, nil)},
		"group's currency":  {"member", lower("USD", delta, nil)},
	}
	// newNode gives acc, in USD, funds and a position in AAPL, an asset the
	// node holds as it holds EUR, and adds the group desk in USD, account bare
	// without a currency, and account member of desk without one of its own.
	newNode := func(t *testing.T) (*localNode, *rebuildProbe, *fakeEngine) {
		t.Helper()
		n, probe, eng := newFundsNode(t,
			domain.Balance{Account: "acc", Asset: "USD", Available: "100"},
			domain.Balance{
				Account: "acc", Asset: "AAPL", Available: "10", AverageEntryPrice: "150",
			},
		)
		if _, err := n.CreateGroup(
			ctx, domain.AccountGroup{Code: "desk", Currency: "USD"}, testCaller,
		); err != nil {
			t.Fatalf("CreateGroup(desk): %v", err)
		}
		for _, account := range []domain.Account{
			{Code: "bare"}, {Code: "member", GroupCode: "desk"},
		} {
			if _, err := n.CreateAccount(ctx, account, testCaller); err != nil {
				t.Fatalf("CreateAccount(%s): %v", account.Code, err)
			}
		}
		return n, probe, eng
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe, eng := newNode(t)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, tc := range requests {
			_, err := n.ApplyAdjustment(
				ctx, tc.account, "", tc.request, domain.MissingAccountReject, agentCaller,
			)
			requireLoosening(t, "ApplyAdjustment("+name+")", err)
		}
		unchanged()
		requireNoAdjustment(t, n, eng)
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _, eng := newNode(t)
			for request, tc := range requests {
				rec, err := n.ApplyAdjustment(
					ctx, tc.account, "", tc.request, domain.MissingAccountReject, caller,
				)
				if err != nil || rec.Accepted == nil {
					t.Fatalf("ApplyAdjustment(%s) = %+v, %v, want it accepted", request, rec, err)
				}
			}
			if len(eng.adjustmentCalls) != len(requests) {
				t.Fatalf(
					"engine adjustment calls = %+v, want the %d requests",
					eng.adjustmentCalls, len(requests),
				)
			}
			if _, ok, err := n.realm.GetAsset(ctx, "GOLD"); err != nil || !ok {
				t.Fatalf("asset GOLD ok=%v err=%v, want it created", ok, err)
			}
		})
	}
}

// vanishingAssetRealm deletes asset code from the memory store right after
// the first read of it returns, as an operator's delete landing between an
// agent's check before provisioning and the account chain would.
type vanishingAssetRealm struct {
	store.RealmStore
	memory *memoryRealm
	code   string
	read   bool
}

func (r *vanishingAssetRealm) GetAsset(
	ctx context.Context, code string,
) (domain.Asset, bool, error) {
	asset, ok, err := r.RealmStore.GetAsset(ctx, code)
	if code == r.code && !r.read {
		r.read = true
		delete(r.memory.assets, code)
	}
	return asset, ok, err
}

// TestLocalNode_AgentAdjustmentOfMissingAssetRefused refuses an agent's
// adjustment of the account's currency while no asset of that code exists -
// one that never did, and one deleted after the check before provisioning -
// as adjusting it would create it, and creates nothing: the asset stays
// missing, the engine is not called, and no adjustment or audit is recorded.
// Every other caller still creates the asset.
func TestLocalNode_AgentAdjustmentOfMissingAssetRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	withdrawal := func(asset string) domain.AdjustmentRequest {
		return domain.AdjustmentRequest{
			Type: domain.AdjustmentTypeWithdrawal, Asset: asset,
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeDelta, Value: "-1",
			},
		}
	}
	// newVaultNode adds account vault, written straight to the store with its
	// own currency XAU, an asset that does not exist.
	newVaultNode := func(t *testing.T) (*localNode, *rebuildProbe, *fakeEngine) {
		t.Helper()
		n, probe, eng := newFundsNode(t)
		if _, err := n.realm.CreateAccount(
			ctx, domain.Account{Code: "vault", Currency: "XAU"},
		); err != nil {
			t.Fatalf("CreateAccount(vault): %v", err)
		}
		return n, probe, eng
	}
	requireAsset := func(t *testing.T, n *localNode, code string, want bool) {
		t.Helper()
		if _, ok, err := n.realm.GetAsset(ctx, code); err != nil || ok != want {
			t.Fatalf("asset %s ok=%v err=%v, want ok=%v", code, ok, err, want)
		}
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe, eng := newVaultNode(t)
		unchanged := refusalKeepsRiskState(t, n, probe)
		_, err := n.ApplyAdjustment(
			ctx, "vault", "", withdrawal("XAU"), domain.MissingAccountReject, agentCaller,
		)
		requireLoosening(t, "ApplyAdjustment(XAU)", err)
		unchanged()
		requireAsset(t, n, "XAU", false)
		if len(eng.adjustmentCalls) != 0 {
			t.Fatalf("engine adjustment calls = %+v, want none", eng.adjustmentCalls)
		}
	})
	t.Run("agent, asset deleted before the chain", func(t *testing.T) {
		t.Parallel()
		n, probe, eng := newFundsNode(t, domain.Balance{
			Account: "acc", Asset: "USD", Available: "100",
		})
		memory, ok := n.realm.(*memoryRealm)
		if !ok {
			t.Fatalf("realm %T, want the memory store", n.realm)
		}
		unchanged := refusalLeavesNoTrace(t, n, probe)
		n.realm = &vanishingAssetRealm{RealmStore: memory, memory: memory, code: "USD"}
		_, err := n.ApplyAdjustment(
			ctx, "acc", "", withdrawal("USD"), domain.MissingAccountReject, agentCaller,
		)
		requireLoosening(t, "ApplyAdjustment(USD deleted)", err)
		unchanged()
		requireAsset(t, n, "USD", false)
		requireNoAdjustment(t, n, eng)
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _, _ := newVaultNode(t)
			rec, err := n.ApplyAdjustment(
				ctx, "vault", "", withdrawal("XAU"), domain.MissingAccountReject, caller,
			)
			if err != nil || rec.Accepted == nil {
				t.Fatalf("ApplyAdjustment = %+v, %v, want it accepted", rec, err)
			}
			requireAsset(t, n, "XAU", true)
		})
	}
}

// TestLocalNode_AgentAdjustmentUnderTradingRuntimeIDRefused refuses an agent's
// adjustment under an id in the trading runtime's namespace, the id the
// runtime books a venue fee under, even one lowering the account's currency,
// as the runtime would take it for the fee booked and never debit the fee: it
// is refused before anything is read, as every read fails here, and nothing is
// recorded. Every other caller's is accepted under that id.
func TestLocalNode_AgentAdjustmentUnderTradingRuntimeIDRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const feeID = domain.ExternalID("trading:venue-1:fee:f-1")
	withdrawal := domain.AdjustmentRequest{
		Type: domain.AdjustmentTypeWithdrawal, Asset: "USD",
		Balance: &domain.AdjustmentAmount{
			Mode: domain.AdjustmentModeDelta, Value: "-1",
		},
	}
	funded := domain.Balance{Account: "acc", Asset: "USD", Available: "100"}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe, eng := newFundsNode(t, funded)
		unchanged := refusalKeepsRiskState(t, n, probe)
		readErr := errors.New("read failure")
		n.realm = &failingAdjustmentReadsRealm{
			RealmStore: n.realm,
			accountErr: readErr, assetErr: readErr, balanceErr: readErr,
		}
		_, err := n.ApplyAdjustment(
			ctx, "acc", feeID, withdrawal, domain.MissingAccountReject, agentCaller,
		)
		if !errors.Is(err, domain.ErrLoosening) || errors.Is(err, readErr) {
			t.Fatalf("ApplyAdjustment(%s) = %v, want ErrLoosening before any read", feeID, err)
		}
		unchanged()
		requireNoAdjustment(t, n, eng)
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _, _ := newFundsNode(t, funded)
			rec, err := n.ApplyAdjustment(
				ctx, "acc", feeID, withdrawal, domain.MissingAccountReject, caller,
			)
			if err != nil || rec.Accepted == nil || rec.ExternalID != feeID {
				t.Fatalf(
					"ApplyAdjustment(%s) = %+v, %v, want it accepted under that id",
					feeID, rec, err,
				)
			}
		})
	}
}

// TestLocalNode_AgentAdjustmentKeepsBooks refuses an agent's adjustment that
// changes the average entry price or the realized P&L of the account's
// currency, and changes nothing, but accepts one naming either with its
// stored value, spelled otherwise too. Every other caller still changes both.
func TestLocalNode_AgentAdjustmentKeepsBooks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stored := domain.Balance{
		Account: "acc", Asset: "USD", Available: "100",
		AverageEntryPrice: "1", RealizedPnl: "5",
	}
	books := func(average, realized string) domain.AdjustmentRequest {
		return domain.AdjustmentRequest{
			Type: domain.AdjustmentTypeCorrection, Asset: "USD",
			AverageEntryPrice: average, RealizedPnl: realized,
		}
	}
	changed := map[string]domain.AdjustmentRequest{
		"average entry price": books("2", ""),
		"realized P&L":        books("", "6"),
	}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe, eng := newFundsNode(t, stored)
		unchanged := refusalKeepsRiskState(t, n, probe)
		for name, request := range changed {
			_, err := n.ApplyAdjustment(
				ctx, "acc", "", request, domain.MissingAccountReject, agentCaller,
			)
			requireLoosening(t, "ApplyAdjustment("+name+")", err)
		}
		unchanged()
		requireNoAdjustment(t, n, eng)
		for name, request := range map[string]domain.AdjustmentRequest{
			"average entry price": books("1.0", ""),
			"realized P&L":        books("", "5.00"),
		} {
			rec, err := n.ApplyAdjustment(
				ctx, "acc", "", request, domain.MissingAccountReject, agentCaller,
			)
			if err != nil || rec.Accepted == nil {
				t.Fatalf("ApplyAdjustment(%s at the stored value) = %+v, %v, want it accepted",
					name, rec, err)
			}
		}
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _, eng := newFundsNode(t, stored)
			for request, adjustment := range changed {
				rec, err := n.ApplyAdjustment(
					ctx, "acc", "", adjustment, domain.MissingAccountReject, caller,
				)
				if err != nil || rec.Accepted == nil {
					t.Fatalf("ApplyAdjustment(%s) = %+v, %v, want it accepted", request, rec, err)
				}
			}
			if len(eng.adjustmentCalls) != len(changed) {
				t.Fatalf(
					"engine adjustment calls = %+v, want the %d changes",
					eng.adjustmentCalls, len(changed),
				)
			}
		})
	}
}

// TestLocalNode_AgentAdjustmentDecidedInsideChain holds the read of the
// stored balance an agent's adjustment is decided on and shows that another
// task on the same account runs only once it is released: the read runs
// inside the account chain, which serializes every change of the account's
// balances, so none lands between the decision and the adjustment.
func TestLocalNode_AgentAdjustmentDecidedInsideChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	eng := newFakeEngine()
	n, probe := newLaneProbeNode(t, eng)
	if _, err := n.CreateAccount(
		ctx, domain.Account{Code: "acc", Currency: "USD"}, testCaller,
	); err != nil {
		t.Fatalf("CreateAccount(acc): %v", err)
	}
	if err := n.realm.UpsertBalance(ctx, domain.Balance{
		Account: "acc", Asset: "USD", Available: "100",
	}); err != nil {
		t.Fatalf("UpsertBalance(USD): %v", err)
	}
	probe.holdBalanceRead.Store(true)
	assertAdministrativeStoreWriteSerialized(t, eng, probe, "acc", func() error {
		_, err := n.ApplyAdjustment(ctx, "acc", "", domain.AdjustmentRequest{
			Type: domain.AdjustmentTypeWithdrawal, Asset: "USD",
			Balance: &domain.AdjustmentAmount{
				Mode: domain.AdjustmentModeAbsolute, Value: "50",
			},
		}, domain.MissingAccountReject, agentCaller)
		return err
	})
}

// failingAdjustmentReadsRealm fails the first read of an account with
// accountErr, the first read of an asset with assetErr and the first read of a
// balance with balanceErr, each when set: the reads an agent's adjustment is
// decided on.
type failingAdjustmentReadsRealm struct {
	store.RealmStore
	accountErr, assetErr, balanceErr error
}

func (r *failingAdjustmentReadsRealm) GetAccount(
	ctx context.Context, code domain.AccountID,
) (domain.Account, bool, error) {
	if err := r.accountErr; err != nil {
		r.accountErr = nil
		return domain.Account{}, false, err
	}
	return r.RealmStore.GetAccount(ctx, code)
}

func (r *failingAdjustmentReadsRealm) GetAsset(
	ctx context.Context, code string,
) (domain.Asset, bool, error) {
	if err := r.assetErr; err != nil {
		r.assetErr = nil
		return domain.Asset{}, false, err
	}
	return r.RealmStore.GetAsset(ctx, code)
}

func (r *failingAdjustmentReadsRealm) GetBalance(
	ctx context.Context, account domain.AccountID, asset string,
) (domain.Balance, bool, error) {
	if err := r.balanceErr; err != nil {
		r.balanceErr = nil
		return domain.Balance{}, false, err
	}
	return r.RealmStore.GetBalance(ctx, account, asset)
}

// TestLocalNode_AgentAdjustmentFailsOnUnreadableState answers an agent's
// adjustment with the failure of a read its decision needs - the account and
// whether the asset exists, read before anything is provisioned, and the
// balance an absolute is compared with in the chain - instead of deciding
// without it, and changes nothing. Only that read fails, and the adjustment
// lowers the account's currency, so any other answer was decided without it.
// A value or a mode the decision cannot read is refused as invalid.
func TestLocalNode_AgentAdjustmentFailsOnUnreadableState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	readErr := errors.New("read failure")
	adjust := func(mode domain.AdjustmentAmountMode, value string) domain.AdjustmentRequest {
		return domain.AdjustmentRequest{
			Type: domain.AdjustmentTypeCorrection, Asset: "USD",
			Balance: &domain.AdjustmentAmount{Mode: mode, Value: value},
		}
	}
	for name, tc := range map[string]struct {
		failed  *failingAdjustmentReadsRealm
		request domain.AdjustmentRequest
		want    error
	}{
		"account": {
			&failingAdjustmentReadsRealm{accountErr: readErr},
			adjust(domain.AdjustmentModeAbsolute, "50"), readErr,
		},
		"asset": {
			&failingAdjustmentReadsRealm{assetErr: readErr},
			adjust(domain.AdjustmentModeAbsolute, "50"), readErr,
		},
		"balance": {
			&failingAdjustmentReadsRealm{balanceErr: readErr},
			adjust(domain.AdjustmentModeAbsolute, "50"), readErr,
		},
		"unparsable value": {
			&failingAdjustmentReadsRealm{},
			adjust(domain.AdjustmentModeDelta, "-1x"), domain.ErrInvalid,
		},
		"unknown mode": {
			&failingAdjustmentReadsRealm{},
			adjust("set", "-1"), domain.ErrInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe, eng := newFundsNode(t, domain.Balance{
				Account: "acc", Asset: "USD", Available: "100",
			})
			unchanged := refusalKeepsRiskState(t, n, probe)
			tc.failed.RealmStore = n.realm
			n.realm = tc.failed
			_, err := n.ApplyAdjustment(
				ctx, "acc", "", tc.request, domain.MissingAccountReject, agentCaller,
			)
			if !errors.Is(err, tc.want) || errors.Is(err, domain.ErrLoosening) {
				t.Fatalf("ApplyAdjustment = %v, want %v and no loosening", err, tc.want)
			}
			unchanged()
			requireNoAdjustment(t, n, eng)
		})
	}
}

// TestLocalNode_AgentCreateAccountRefused refuses an agent's account, as a new
// account starts unblocked, with no barrier of its own and no balance or P&L
// history, and changes nothing: neither the account nor its currency asset is
// created. Every other caller creates both.
func TestLocalNode_AgentCreateAccountRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fresh := domain.Account{Code: "fresh", Currency: "CHF"}

	t.Run("agent", func(t *testing.T) {
		t.Parallel()
		n, probe := newRebuildProbeNode(t, newResolvingEngine())
		unchanged := refusalKeepsRiskState(t, n, probe)
		_, err := n.CreateAccount(ctx, fresh, agentCaller)
		requireLoosening(t, "CreateAccount", err)
		unchanged()
	})
	for name, caller := range operatorCallers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, _ := newRebuildProbeNode(t, newResolvingEngine())
			if _, err := n.CreateAccount(ctx, fresh, caller); err != nil {
				t.Fatalf("CreateAccount: %v", err)
			}
			if account, ok, err := n.realm.GetAccount(ctx, "fresh"); err != nil || !ok ||
				account.Currency != "CHF" {
				t.Fatalf("account fresh = %+v ok=%v err=%v, want it in CHF", account, ok, err)
			}
		})
	}
}

// TestLocalNode_AgentAutoCreateAccountRefused refuses an agent's order, drop
// copy, adjustment and account-scope rate barrier naming ghost, an account
// Officer does not know yet, with missingAccount create, as registering ghost
// would start a new account, and changes nothing: neither ghost nor the asset
// the call names is created. Each passes for acc, which exists; the
// adjustment names USD, acc's currency and the one asset an agent may adjust.
func TestLocalNode_AgentAutoCreateAccountRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	order := func(account domain.AccountID, base string, dropCopy bool) domain.Order {
		return domain.Order{
			Account: account, DropCopy: dropCopy,
			BaseAsset: base, QuoteAsset: "USD", Side: domain.OrderSideBuy,
			AmountKind: domain.OrderAmountKindQuantity, AmountValue: "1", Price: "100",
		}
	}
	for name, call := range map[string]func(
		n *localNode, account domain.AccountID, asset string,
	) error{
		"order": func(n *localNode, account domain.AccountID, asset string) error {
			_, err := n.SubmitOrder(
				ctx, order(account, asset, false), domain.MissingAccountCreate, agentCaller,
			)
			return err
		},
		"drop copy": func(n *localNode, account domain.AccountID, asset string) error {
			_, err := n.SubmitOrder(
				ctx, order(account, asset, true), domain.MissingAccountCreate, agentCaller,
			)
			return err
		},
		"adjustment": func(n *localNode, account domain.AccountID, _ string) error {
			_, err := n.ApplyAdjustment(ctx, account, "", domain.AdjustmentRequest{
				Type: domain.AdjustmentTypeWithdrawal, Asset: "USD",
				Balance: &domain.AdjustmentAmount{
					Mode: domain.AdjustmentModeDelta, Value: "-1",
				},
			}, domain.MissingAccountCreate, agentCaller)
			return err
		},
		"rate barrier": func(n *localNode, account domain.AccountID, _ string) error {
			_, err := n.PutRateLimit(
				ctx, rateLimit(domain.ScopeAccount, account, "", 10, time.Second),
				domain.MissingAccountCreate, agentCaller,
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			n, probe := newLimitNode(t, AccountLimits{RateLimits: []domain.LimitRate{
				rateLimit(domain.ScopeBroker, "", "", 100, time.Second),
			}})
			if _, err := n.CreateAccount(
				ctx, domain.Account{Code: "acc", Currency: "USD"}, testCaller,
			); err != nil {
				t.Fatalf("CreateAccount(acc): %v", err)
			}
			unchanged := refusalKeepsRiskState(t, n, probe)
			requireLoosening(t, name+"(ghost)", call(n, "ghost", "MSFT"))
			unchanged()
			if err := call(n, "acc", "AAPL"); err != nil {
				t.Fatalf("%s(acc): %v", name, err)
			}
		})
	}
}
