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
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
)

// tightenOnlyEntry classifies one mutating MCP command against the
// tighten-only rule TestEveryMutatingMCPCommandIsTightenOnlyClassified holds.
type tightenOnlyEntry struct {
	// reason says, in one or two sentences, why the command cannot loosen risk
	// state or widen what an agent may do, or which core refusal stops it and
	// where (file and function), or, for an execution the agent reports, the
	// owner's decision that leaves its defence to signed trusted ingress.
	reason string
	// proof drives a call the core refuses for an agent through the real MCP
	// server into a real local service and requires loosening_refused with the
	// risk state, read back through the service, unchanged. Every command the
	// core refuses for an agent in some case carries one.
	proof func(t *testing.T)
}

// tightenOnlyClassification holds one entry per mutating command of the
// Officer MCP catalogue, keyed by command name.
var tightenOnlyClassification = map[string]tightenOnlyEntry{
	"set_limit": {
		reason: "The core refuses a barrier looser than the one it replaces or " +
			"overrides, and the first order-size barrier while a rate-limit barrier " +
			"is configured, as its engine rebuild resets every rate window " +
			"(framework/node/local_limits.go: PutRateLimit, guardTightenOnlyOrderSize, " +
			"guardTightenOnlySpotFundsPnlBounds, through the NoLooserThan methods of " +
			"framework/domain/limits.go); the tool removes no barrier.",
		proof: TestSetLimitRateOnControlPlane,
	},
	"create_account": {
		reason: "The core refuses every account creation by an agent, as a new " +
			"account starts unblocked, with no barrier of its own and no balance or " +
			"P&L history (framework/node/local_autocreate.go: " +
			"guardTightenOnlyAccountCreation, from framework/node/local_accounts.go: " +
			"CreateAccount).",
		proof: agentAccountCreationRefused,
	},
	"update_account": {
		reason: "The core refuses an agent's code change of an account that is " +
			"blocked, in a blocked group or in a group with a group-scoped Spot Funds " +
			"P&L bound, or that carries a barrier of its own " +
			"(framework/node/local_accounts.go: guardTightenOnlyAccountRelease, from " +
			"UpdateAccount); a title change moves no risk state.",
		proof: TestBlockedAccountCodeNeverComesBackUnblocked,
	},
	"delete_account": {
		reason: "The core refuses an agent's delete, with or without force, of an " +
			"account guardTightenOnlyAccountRelease protects, and of any account " +
			"while a rate-limit barrier is configured, as the engine rebuild would " +
			"reset every rate window (framework/node/local_accounts.go: " +
			"DeleteAccount; framework/node/local.go: guardTightenOnlyRebuild).",
		proof: TestDeleteAccountToolRefusesBlockedAccount,
	},
	"set_account_currency": {
		reason: "The core changes an account's effective realized P&L currency " +
			"only while the account is economically empty and no Spot Funds P&L " +
			"bound applies to it, and refuses it otherwise with " +
			"currency_change_blocked for every caller " +
			"(framework/node/local_currency.go: SetAccountCurrency), so no balance, " +
			"bound or block the account is subject to moves.",
	},
	"apply_adjustment": {
		reason: "An agent may only lower the balance, held or incoming of the " +
			"account's own currency: the core refuses another asset, a raise, an " +
			"average entry price or a realized P&L other than the stored one, an " +
			"account or asset the adjustment would create, and an id in the trading " +
			"runtime's namespace (framework/node/local_adjustments.go: " +
			"ApplyAdjustment, guardTightenOnlyAdjustment, through " +
			"framework/domain/limits.go: AdjustmentRequest.NoLooserThan and " +
			"AdjustmentIDOutsideTradingRuntime).",
		proof: func(t *testing.T) {
			t.Parallel()
			t.Run("raising delta", TestApplyAdjustmentOnlyLowersOnControlPlane)
			t.Run("position, average entry price and realized P&L",
				TestApplyAdjustmentRefusesPositionAndBooksOnControlPlane)
			t.Run("trading runtime id", TestApplyAdjustmentRefusesTradingRuntimeIDOnControlPlane)
		},
	},
	"create_asset": {
		reason: "A new asset starts with no barrier, balance or block of its own, " +
			"and an order an agent submits creates the assets it names anyway " +
			"(framework/node/local_orders.go: ensureAutoCreatedAssets). A code that " +
			"delete_asset or update_asset freed carried no barrier, as the core " +
			"refuses an agent's release of an asset a barrier references " +
			"(framework/node/local_assets.go: guardTightenOnlyAssetRelease), and no " +
			"barrier is keyed by asset class.",
	},
	"update_asset": {
		reason: "The core refuses an agent's code change of an asset a rate-limit " +
			"or order-size barrier references or a Spot Funds P&L bound is " +
			"denominated in (framework/node/local_assets.go: " +
			"guardTightenOnlyAssetRelease, from UpdateAsset); the title and the " +
			"asset class key no barrier.",
		proof: TestUpdateAssetToolRefusesCodeChangeOfBarrierAsset,
	},
	"delete_asset": {
		reason: "The core refuses an agent's forced delete outright, as force " +
			"deletes the asset's balances, orders, trades, adjustments and barriers " +
			"with it, and a delete without force of an asset a barrier references " +
			"(framework/node/local_assets.go: DeleteAsset, " +
			"guardTightenOnlyAssetRelease).",
		proof: agentForcedAssetDeleteRefused,
	},
	"create_group": {
		reason: "A new group starts empty, and no MCP command puts an account into " +
			"a group - create_account is refused for an agent and update_account " +
			"changes only a code and a title - so a new group, unblocked and " +
			"without a bound, governs no account. A code delete_group freed carried " +
			"no block or bound, as the core refuses an agent's delete of a blocked " +
			"or bounded group (framework/node/local_groups.go: " +
			"guardTightenOnlyGroupDelete).",
	},
	"update_group": {
		reason: "A rename keeps the group's stable id, by which its members, its " +
			"block, its group-scoped bounds and its trading grants are linked, so " +
			"they all move with it (framework/node/local_groups.go: UpdateGroup); " +
			"the code it frees comes back only as a new, empty group, and a title " +
			"change moves no risk state.",
	},
	"delete_group": {
		reason: "The core refuses an agent's delete, with or without force, of a " +
			"blocked group, of a group carrying a group-scoped Spot Funds P&L bound, " +
			"and of any group while a rate-limit barrier is configured, as the " +
			"engine rebuild would reset every rate window " +
			"(framework/node/local_groups.go: DeleteGroup, " +
			"guardTightenOnlyGroupDelete; framework/node/local.go: " +
			"guardTightenOnlyRebuild).",
		proof: TestDeleteGroupToolRefusesBlockedGroup,
	},
	"set_market_data_instrument": {
		reason: "The core refuses an agent's enable, as it widens the quotes the " +
			"engine prices orders with (framework/node/local_marketdata.go: " +
			"SetMarketDataInstrumentEnabled); a disable narrows them.",
		proof: TestSetMarketDataInstrumentToolRefusesEnable,
	},
	"submit_order": {
		reason: "An order passes the full pre-trade pipeline, every barrier and " +
			"block, and the core refuses an agent's order naming an account that " +
			"does not exist yet (framework/node/local_autocreate.go: " +
			"ensureAutoCreatedAccount). The fill the immediate mode settles at the " +
			"order's own price is an execution the agent reports, left as it is by " +
			"the owner's decision: its defence is signed trusted ingress, pending " +
			"work of its own.",
		proof: TestDeletedAccountCodeNeverComesBack,
	},
	"submit_drop_copy_order": {
		reason: "A drop-copy reports an execution that already happened, so it " +
			"ignores policy rejects and blocks; its economics are an execution the " +
			"agent reports, left as they are by the owner's decision, with signed " +
			"trusted ingress as their defence, pending work of its own. The core " +
			"refuses an agent's drop-copy naming an account that does not exist yet " +
			"(framework/node/local_autocreate.go: ensureAutoCreatedAccount).",
		proof: agentDropCopyForUnknownAccountRefused,
	},
	"confirm_execution": {
		reason: "It records confirmation history for an order Officer approved " +
			"under its own signed hold and never calls the engine or changes the " +
			"order's status or balances (framework/backend/approval.go: " +
			"Service.ConfirmExecution), so no reservation, barrier or block moves.",
	},
	"cancel": {
		reason: "The core applies a cancel as a terminal execution report with " +
			"status cancelled, and only for an untouched order Officer approved under " +
			"its own signed hold - no execution-report activity, not venue-linked - " +
			"with the engine releasing that order's own recorded reservation, not an " +
			"amount the caller gives (framework/node/local_orders.go: CancelOrder, " +
			"cancelOrder). That the order went unfilled is therefore an execution the " +
			"agent reports, left as it is by the owner's decision, with signed " +
			"trusted ingress as its defence, pending work of its own.",
	},
	"report_fill": {
		reason: "An execution report is an execution the agent reports, left as " +
			"it is by the owner's decision: its defence is signed trusted ingress, " +
			"pending work of its own. It applies only to an order submit_order or " +
			"submit_drop_copy_order recorded, never to a terminal or venue-linked " +
			"one.",
	},
	"send_paper_order": {
		reason: "It holds the order through the full pre-trade pipeline with a " +
			"missing account refused, and only through a paper connection already " +
			"granted to the account (framework/backend/backend_trading.go: " +
			"Service.sendOrder), so it takes no exposure a barrier or block refuses " +
			"and reaches no venue the agent was not given.",
	},
	"create_trading_connection": {
		reason: "The core refuses every connection creation by an agent before the " +
			"venue is called, as a new connection widens what can be sent " +
			"(framework/backend/backend_trading.go: " +
			"Service.CreateTradingConnection).",
		proof: agentTradingChangeRefused("create_trading_connection",
			func(string) map[string]any {
				return map[string]any{
					"provider": "venue", "label": "Second", "mode": "paper",
					"credentials": map[string]any{"apiKey": "second-key", "apiSecret": "second-secret"},
					"enabled":     true,
				}
			}, nil),
	},
	"update_trading_connection": {
		reason: "The core refuses an agent's credential change before the venue " +
			"is called, as it restarts the trading runtime and so brings unavailable " +
			"connections back (framework/backend/backend_trading.go: " +
			"Service.UpdateTradingConnectionSettings); a label change moves no risk " +
			"state.",
		proof: agentTradingChangeRefused("update_trading_connection",
			func(connection string) map[string]any {
				return map[string]any{
					"connection":  connection,
					"credentials": map[string]any{"apiKey": "second-key", "apiSecret": "second-secret"},
				}
			}, nil),
	},
	"set_trading_connection_enabled": {
		reason: "The core refuses an agent's enable, as it widens what can be sent " +
			"(framework/node/local_trading.go: SetTradingConnectionEnabled); a " +
			"disable narrows it.",
		proof: agentTradingChangeRefused("set_trading_connection_enabled",
			func(connection string) map[string]any {
				return map[string]any{"connection": connection, "enabled": true}
			},
			func(t *testing.T, service *backend.Service, connection domain.ExternalID) {
				t.Helper()
				if err := service.SetTradingConnectionEnabled(
					systemCtx(), connection.String(), false,
				); err != nil {
					t.Fatalf("disable connection %s: %v", connection, err)
				}
			}),
	},
	"set_trading_instrument": {
		reason: "The core refuses every mapping upsert by an agent, as a mapping " +
			"decides what can be sent and which asset a venue fee is booked in " +
			"(framework/node/local_trading.go: UpsertTradingInstrument).",
		proof: agentTradingChangeRefused("set_trading_instrument",
			func(connection string) map[string]any {
				return map[string]any{
					"connection": connection, "externalSymbol": "MSFT",
					"baseAsset": "MSFT", "quoteAsset": "USD", "enabled": true,
				}
			}, nil),
	},
	"delete_trading_instrument": {
		reason: "The core refuses every mapping delete by an agent, as a removed " +
			"mapping can leave a venue fee's asset unresolved and the fee skipped " +
			"(framework/node/local_trading.go: DeleteTradingInstrument).",
		proof: agentTradingChangeRefused("delete_trading_instrument",
			func(connection string) map[string]any {
				return map[string]any{"connection": connection, "externalSymbol": "AAPL"}
			}, nil),
	},
	"grant_trading_access": {
		reason: "The core refuses every grant by an agent, as a grant widens what " +
			"its account or group may send to (framework/node/local_trading.go: " +
			"PutTradingAccess).",
		proof: agentTradingChangeRefused("grant_trading_access",
			func(connection string) map[string]any {
				return map[string]any{"connection": connection, "account": "other"}
			}, operatorSetsFeeAccount),
	},
	"revoke_trading_access": {
		reason: "A revoke narrows what can be sent; the core refuses an agent's " +
			"revoke of the last account a connection without a fee account reaches " +
			"on a provider with unlinked fees, as its venue fees would then be " +
			"skipped instead of debited (framework/node/local_trading.go: " +
			"RemoveTradingAccess).",
		proof: agentTradingChangeRefused("revoke_trading_access",
			func(connection string) map[string]any {
				return map[string]any{"connection": connection, "account": "desk"}
			}, nil),
	},
	"set_trading_fee_account": {
		reason: "The core refuses every fee-account set by an agent, as it moves " +
			"where future venue fees are debited and lets the connection take a " +
			"group grant or a grant to a second account " +
			"(framework/node/local_trading.go: SetTradingConnectionFeeAccount).",
		proof: agentTradingChangeRefused("set_trading_fee_account",
			func(connection string) map[string]any {
				return map[string]any{"connection": connection, "account": "fees"}
			}, nil),
	},
	"clear_trading_fee_account": {
		reason: "The core refuses every fee-account clear by an agent, as it moves " +
			"where future venue fees are debited (framework/node/local_trading.go: " +
			"SetTradingConnectionFeeAccount).",
		proof: agentTradingChangeRefused("clear_trading_fee_account",
			func(connection string) map[string]any {
				return map[string]any{"connection": connection}
			}, operatorSetsFeeAccount),
	},
}

// operatorSetsFeeAccount sets fees as the fee account of connection as the
// operator, so the connection may take a grant to a second account.
func operatorSetsFeeAccount(t *testing.T, service *backend.Service, connection domain.ExternalID) {
	t.Helper()
	if err := service.SetTradingFeeAccount(systemCtx(), connection, "fees"); err != nil {
		t.Fatalf("set the fee account of %s: %v", connection, err)
	}
}

// TestEveryMutatingMCPCommandIsTightenOnlyClassified holds the tighten-only
// rule for the whole Officer MCP catalogue: an agent may only tighten risk
// state - barriers and blocks - and narrow what it may do, never loosen or
// widen it. Every command the catalogue registers as mutating therefore needs
// an entry in tightenOnlyClassification. Its reason says why the command
// cannot loosen or widen, or which core refusal stops it and where, file and
// function, or, for an execution the agent reports, the owner's decision that
// leaves its defence to signed trusted ingress. Whenever the core refuses the
// command for an agent in some case, its proof drives one such case through
// the real MCP server into a real local service and requires loosening_refused
// with the risk state unchanged. The test fails naming every mutating command
// without an entry, every entry naming a command that is not in the catalogue
// or does not mutate, and every entry without a reason; it runs every proof as
// a subtest named after its command.
func TestEveryMutatingMCPCommandIsTightenOnlyClassified(t *testing.T) {
	t.Parallel()
	mutating := make(map[string]bool)
	for _, d := range officerToolRegistry().Descriptors() {
		mutating[d.Name] = d.Mutating
	}
	for _, name := range slices.Sorted(maps.Keys(mutating)) {
		if _, classified := tightenOnlyClassification[name]; mutating[name] && !classified {
			t.Errorf("mutating MCP command %s is not classified against the tighten-only rule: "+
				"add its entry to tightenOnlyClassification", name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(tightenOnlyClassification)) {
		entry := tightenOnlyClassification[name]
		if isMutating, listed := mutating[name]; !listed {
			t.Errorf("tightenOnlyClassification entry %s names no command of the catalogue", name)
		} else if !isMutating {
			t.Errorf("tightenOnlyClassification entry %s names a command that does not mutate", name)
		}
		if strings.TrimSpace(entry.reason) == "" {
			t.Errorf("tightenOnlyClassification entry %s has no reason", name)
		}
		if entry.proof != nil {
			t.Run(name, entry.proof)
		}
	}
}

// requireLooseningRefused fails the test, naming what, unless res is a tool
// error opening with loosening_refused.
func requireLooseningRefused[Out any](t *testing.T, what string, res *toolResult[Out]) {
	t.Helper()
	if text := textContent(res.Content); !res.IsError || !strings.HasPrefix(text, "loosening_refused: ") {
		t.Fatalf("%s = %q (tool error %t), want loosening_refused", what, text, res.IsError)
	}
}

// agentAccountCreationRefused drives create_account through the control-plane
// Source into a local service: the core refuses the agent's creation of an
// account under a code no account holds with loosening_refused, so no account
// takes the code.
func agentAccountCreationRefused(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)

	requireLooseningRefused(t, "create_account of a new code",
		callTool[map[string]any](t, src, "create_account", map[string]any{"code": "fresh"}))
	_, _, err := service.GetAccountState(systemCtx(), "fresh")
	requireGone(t, "account fresh", err)
}

// agentForcedAssetDeleteRefused drives delete_asset through the control-plane
// Source into a local service: the core refuses the agent's forced delete of
// an asset with loosening_refused, as force would delete what depends on it,
// and keeps the asset stored.
func agentForcedAssetDeleteRefused(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "ETH"}); err != nil {
		t.Fatalf("create asset ETH: %v", err)
	}

	requireLooseningRefused(t, "delete_asset with force",
		callTool[map[string]any](t, src, "delete_asset", map[string]any{"asset": "ETH", "force": true}))
	if _, ok := storedAsset(t, service, "ETH"); !ok {
		t.Fatal("asset ETH is gone after the refusal")
	}
}

// agentDropCopyForUnknownAccountRefused drives submit_drop_copy_order through
// the control-plane Source into a local service with a signing key: the core
// refuses the agent's drop-copy naming an account that does not exist yet,
// which missingAccount create would register, with loosening_refused, so no
// account takes the code.
func agentDropCopyForUnknownAccountRefused(t *testing.T) {
	t.Parallel()
	service, chain, _ := reportFillChain(t)

	requireLooseningRefused(t, "submit_drop_copy_order naming an unknown account",
		callSubmitDropCopyOrder(t, enabledControlPlaneSource{chain}, submitDropCopyOrderInput{
			Account: "ghost", BaseAsset: "AAPL", QuoteAsset: "USD", Side: "buy",
			AmountKind: "quantity", AmountValue: "1", Price: "150", MissingAccount: "create",
		}))
	_, _, err := service.GetAccountState(systemCtx(), "ghost")
	requireGone(t, "account ghost", err)
}

// tradingControlPlaneTools builds the Officer service over a local node with
// the venue runtime of the account-scope tests, whose one provider keeps
// unlinked fees and whose account check and restart succeed, so a create,
// credential change or enable the core failed to refuse would change the
// stored state, and the tool Source over it. The operator's paper connection
// is stored through the node with venue identity venue-1, apart from the
// venue-2 that runtime answers for a new connection; it maps AAPL, grants desk
// and has no fee account. The accounts desk, other and
// fees and the assets USD, AAPL and MSFT exist. The node is returned beside the
// service, as only the node reads a connection's credentials back.
func tradingControlPlaneTools(
	t *testing.T,
) (*backend.Service, node.Node, frameworkmcp.Source, domain.ExternalID) {
	t.Helper()
	ctx := systemCtx()
	target, _ := newLocalNode(t)
	service := newOfficerService(t, target, nil, nil, backend.WithTradingRuntime(scopeTradingRuntime{}))
	for _, code := range []string{"USD", "AAPL", "MSFT"} {
		if _, err := service.CreateAsset(ctx, domain.Asset{Code: code}); err != nil {
			t.Fatalf("create asset %s: %v", code, err)
		}
	}
	for _, code := range []domain.AccountID{"desk", "other", "fees"} {
		if _, err := service.CreateAccount(ctx, domain.Account{Code: code}); err != nil {
			t.Fatalf("create account %s: %v", code, err)
		}
	}
	connection, err := target.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: "venue", Label: "Paper", Mode: domain.TradingModePaper,
		VenueIdentity: "venue-1", Enabled: true,
		Credentials: `{"apiKey":"key-value","apiSecret":"secret-value"}`,
	}, auth.SystemCaller())
	if err != nil {
		t.Fatalf("create trading connection: %v", err)
	}
	if err := service.UpsertTradingInstrument(ctx, domain.TradingInstrument{
		Connection: connection.ExternalID, ExternalSymbol: "AAPL",
		BaseAsset: "AAPL", QuoteAsset: "USD", Enabled: true,
	}); err != nil {
		t.Fatalf("map AAPL: %v", err)
	}
	if err := service.PutTradingAccess(ctx, domain.TradingAccess{
		Account: "desk", Connection: connection.ExternalID,
	}); err != nil {
		t.Fatalf("grant desk: %v", err)
	}
	return service, target, enabledControlPlaneSource{frameworkmcp.SourceFor(service)}, connection.ExternalID
}

// agentTradingChangeRefused returns the proof that the core refuses an agent's
// call of tool, with the input in builds for the connection of
// tradingControlPlaneTools, with loosening_refused, and keeps the trading
// configuration the service reports and the connection the node stores, its
// credentials included, as they were. prepare, when set, first changes the
// connection as the operator.
func agentTradingChangeRefused(
	tool string,
	in func(connection string) map[string]any,
	prepare func(t *testing.T, service *backend.Service, connection domain.ExternalID),
) func(*testing.T) {
	return func(t *testing.T) {
		t.Parallel()
		ctx := systemCtx()
		service, target, src, connection := tradingControlPlaneTools(t)
		if prepare != nil {
			prepare(t, service, connection)
		}
		// stored reads the trading configuration the service reports and the
		// connection row the node stores.
		stored := func() (backend.TradingStatus, domain.TradingConnection) {
			t.Helper()
			status, err := service.GetTrading(ctx)
			if err != nil {
				t.Fatalf("read trading: %v", err)
			}
			row, found, err := target.GetTradingConnection(ctx, connection)
			if err != nil || !found {
				t.Fatalf("read connection %s: found=%t err=%v", connection, found, err)
			}
			return status, row
		}
		status, row := stored()

		requireLooseningRefused(t, tool, callTool[map[string]any](t, src, tool, in(connection.String())))
		if gotStatus, gotRow := stored(); !reflect.DeepEqual(gotStatus, status) || !reflect.DeepEqual(gotRow, row) {
			t.Fatalf("%s refusal left trading %+v and connection %+v, want %+v and %+v unchanged",
				tool, gotStatus, gotRow, status, row)
		}
	}
}
