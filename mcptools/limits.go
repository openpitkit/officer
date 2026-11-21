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

package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

const getSpotFundsLimitModeToolName = "get_spot_funds_limit_mode"

const getSpotFundsLimitModeToolDescription = "Return the global Spot Funds limit mode " +
	"and the accounts a Spot Funds P&L bound blocks for want of a currency. " +
	"On a shortfall, enforce refuses, review_with_hold requests review with a full hold, " +
	"review_without_hold requests review without a pending hold, and track_only " +
	"records the hold even into negative available funds. stored=false means no row " +
	"and the default enforce. P&L-bound, rate, order-size and position barriers " +
	"apply in all four modes. " +
	"accountsWithoutCurrency lists the visible accounts with no effective " +
	"realized P&L currency that a Spot Funds P&L bound applies to: their P&L " +
	"halts, so the bound blocks them. Read-only - no secrets, no order-flow control."

type getSpotFundsLimitModeInput struct{}

func getSpotFundsLimitModeHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[getSpotFundsLimitModeInput, httpx.SpotFundsLimitModeDTO] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		_ getSpotFundsLimitModeInput,
	) (string, httpx.SpotFundsLimitModeDTO, error) {
		state, err := src.SpotFundsLimitMode(ctx)
		if err != nil {
			return "", httpx.SpotFundsLimitModeDTO{}, toolError(err)
		}
		return fmt.Sprintf("Spot Funds limit mode: %s", state.Mode), httpx.ToSpotFundsLimitModeDTO(state), nil
	}
}

// The limit tools accept the flat fields of the corresponding REST body,
// except rule mode: for policies identified by domain.LimitPolicyHasMode,
// they address only reject barriers.
// They pass the fields to the core with the explicit write intent. The core
// validates the values and missing-account policy and enforces tighten-only
// writes.

const addLimitToolName = "add_limit"
const updateLimitToolName = "update_limit"

const keyedLimitPoliciesDescription = "rate_limit, order_size_limit, position_limit, " +
	"spot_funds_pnl_bounds_kill_switch (realized P&L), " +
	"spot_funds_unrealized_pnl_bounds_kill_switch (unrealized P&L) and " +
	"spot_funds_total_pnl_bounds_kill_switch (total P&L)"

const limitPolicyFieldsDescription = "The other arguments are that " +
	"kind's fields. A field of another kind is refused with validation. " +
	"Writes to rate, order-size, position and P&L-bound policies address the reject " +
	"barrier and refuse a mode argument. Review barriers are read-only for an agent. "

const limitAccountCurrencyDescription = "An account P&L bound must use the account's effective " +
	"currency: its own, else its group's, else the default group's. A different " +
	"bound currency or no resolved currency is refused with pnl_bound_currency_mismatch. "

const limitLooseningDescription = "Anything that loosens risk is refused with " +
	"loosening_refused and changes nothing. "

const addLimitToolDescription = "Create a risk barrier at a new address and return it as stored. " +
	"If a barrier already exists at that address, limit_exists is returned and nothing changes. " +
	"Use update_limit to change a stored barrier. " +
	"The policy argument accepts " + keyedLimitPoliciesDescription + ". " +
	limitPolicyFieldsDescription + limitLooseningDescription +
	"A new account_underlying_asset or account_settlement_asset cap " +
	"must be no higher than the underlying_asset or settlement_asset cap on " +
	"the same asset that it overrides. A new account_group or account P&L " +
	"bound must be in the currency of, and no looser than, the bound it takes " +
	"over from. A group takes over from the global bound, and an account takes " +
	"over from its currently applicable bound. " + limitAccountCurrencyDescription +
	"The first reject order-size barrier is refused while any rate-limit barrier is configured. " +
	"Mutates Officer control-plane config; protected and disabled by default."

const updateLimitToolDescription = "Change the risk barrier stored at the given address and return it as stored. " +
	"For keyed policies, if no barrier exists there, not_found is returned. Use add_limit to create one. " +
	"The policy argument accepts " + keyedLimitPoliciesDescription + ", or spot_funds_limit_mode. " +
	limitPolicyFieldsDescription + limitLooseningDescription +
	"For rate_limit, allow no more orders and keep, on the broker and asset " +
	"scopes, the same window, on the account and account_asset scopes a " +
	"window no shorter. For order_size_limit, keep every current cap and raise none. " +
	"For position_limit, keep the ceiling at or below its current value. " +
	"For a P&L-bound kill switch, keep the same currency and every current bound. " +
	"The lower bound can only stay the same or rise, and the upper bound can only stay the same or fall. " +
	limitAccountCurrencyDescription +
	"spot_funds_limit_mode is global and takes the one field mode. This tool " +
	"accepts enforce or track_only and refuses a switch down to track_only with " +
	"loosening_refused. review_with_hold and review_without_hold are set through " +
	"REST or the panel; this tool returns validation for either. Switching to enforce " +
	"resets no balance. P&L-bound, rate, order-size and position barriers apply in " +
	"all four modes. No barrier can be removed. Mutates " +
	"Officer control-plane config; protected and disabled by default."

// limitWriteInput is the REST body of the named policy's write, flattened next
// to the policy, with REST's missingAccount query value, except rule mode:
// policies identified by domain.LimitPolicyHasMode address only reject barriers.
type limitWriteInput struct {
	Policy         string `json:"policy" jsonschema:"Barrier kind: rate_limit, order_size_limit, position_limit, spot_funds_pnl_bounds_kill_switch, spot_funds_unrealized_pnl_bounds_kill_switch, spot_funds_total_pnl_bounds_kill_switch or spot_funds_limit_mode"`
	Scope          string `json:"scope" jsonschema:"Barrier scope: broker, asset, account or account_asset for rate_limit; broker, underlying_asset, settlement_asset, account_underlying_asset or account_settlement_asset for order_size_limit; account_asset for position_limit; global, account_group or account for the P&L-bound kill switches; global for spot_funds_limit_mode"`
	Account        string `json:"account,omitempty" jsonschema:"Account code, for a scope with an account axis; omitted otherwise"`
	Asset          string `json:"asset,omitempty" jsonschema:"rate_limit and order_size_limit: asset code, for a scope with an asset axis - the underlying asset for a quantity cap, the settlement asset for a notional cap; position_limit: the asset whose position is bounded; omitted otherwise"`
	AccountGroup   string `json:"accountGroup,omitempty" jsonschema:"P&L-bound kill switches: account group code, for the account_group scope"`
	WindowMs       int64  `json:"windowMs,omitempty" jsonschema:"rate_limit: window in milliseconds, fixed on the broker and asset scopes and rolling on the account and account_asset scopes"`
	MaxOrders      uint64 `json:"maxOrders,omitempty" jsonschema:"rate_limit: most orders admitted within the window"`
	MaxQuantity    string `json:"maxQuantity,omitempty" jsonschema:"order_size_limit: per-order quantity cap as an exact decimal string; a broker barrier carries either cap or both, the underlying-asset scopes this one only"`
	MaxNotional    string `json:"maxNotional,omitempty" jsonschema:"order_size_limit: per-order notional cap as an exact decimal string; the settlement-asset scopes carry this one only"`
	MaxPosition    string `json:"maxPosition,omitempty" jsonschema:"position_limit: non-negative ceiling on the worst-case position as an exact decimal string in plain notation, without an exponent; zero is valid"`
	Currency       string `json:"currency,omitempty" jsonschema:"P&L-bound kill switches: code of an existing asset that denominates both bounds"`
	LowerBound     string `json:"lowerBound,omitempty" jsonschema:"P&L-bound kill switches: lower P&L bound as an exact decimal string in plain notation, without an exponent"`
	UpperBound     string `json:"upperBound,omitempty" jsonschema:"P&L-bound kill switches: upper P&L bound, written as lowerBound is; at least one bound is set"`
	Mode           string `json:"mode,omitempty" jsonschema:"spot_funds_limit_mode only: enforce or track_only. A switch down to track_only returns loosening_refused. Set review_with_hold or review_without_hold through REST or the panel"`
	MissingAccount string `json:"missingAccount,omitempty" jsonschema:"Needed when account is named: create or reject. For a missing account, add_limit refuses create with loosening_refused, and reject refuses the unknown account with account_missing unless another refusal comes first. update_limit returns not_found for an absent barrier before handling this option. Ignored when no account is named"`
	// keys are the argument names the call sent, as it spelled them.
	keys []string
}

// UnmarshalJSON decodes the arguments as the SDK decodes those of every tool,
// refusing an undeclared one, and keeps the names the call sent: an argument
// sent empty, zero or null decodes as one omitted, and refuseForeignFields
// reads the names to refuse it all the same.
func (in *limitWriteInput) UnmarshalJSON(data []byte) error {
	type limitWriteArguments limitWriteInput
	var args limitWriteArguments
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return err
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(data, &sent); err != nil {
		return err
	}
	*in = limitWriteInput(args)
	in.keys = slices.Collect(maps.Keys(sent))
	return nil
}

// sent reports whether the call sent the argument name, matching its key
// ignoring case as encoding/json matches a key to a field.
func (in limitWriteInput) sent(name string) bool {
	return slices.ContainsFunc(in.keys, func(key string) bool {
		return strings.EqualFold(key, name)
	})
}

// refuseForeignFields refuses an argument of another policy than the one in
// names, sent with any value, as no body of that policy carries it. The Spot
// Funds limit-mode meta-policy carries no axis, so even account is foreign to
// it.
func (in limitWriteInput) refuseForeignFields() error {
	rate := domain.PolicyRateLimit
	size := domain.PolicyOrderSizeLimit
	position := domain.PolicyPositionLimit
	bounds := []string{
		domain.PolicySpotFundsPnlBoundsKillSwitch,
		domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
		domain.PolicySpotFundsTotalPnlBoundsKillSwitch,
	}
	mode := domain.PolicySpotFundsLimitMode
	for _, field := range []struct {
		name     string
		policies []string
	}{
		{"account", append([]string{rate, size, position}, bounds...)},
		{"asset", []string{rate, size, position}},
		{"accountGroup", bounds},
		{"windowMs", []string{rate}},
		{"maxOrders", []string{rate}},
		{"maxQuantity", []string{size}},
		{"maxNotional", []string{size}},
		{"maxPosition", []string{position}},
		{"currency", bounds},
		{"lowerBound", bounds},
		{"upperBound", bounds},
		{"mode", []string{mode}},
	} {
		if in.sent(field.name) && !slices.Contains(field.policies, in.Policy) {
			return domain.NewValidationError(
				"/"+field.name, "unknown_field",
				fmt.Sprintf("%s is not a field of policy %s", field.name, in.Policy),
			)
		}
	}
	return nil
}

func addLimitHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[limitWriteInput, httpx.PutLimitResultDTO] {
	return writeLimitHandler(src, domain.LimitWriteAdd)
}

func updateLimitHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[limitWriteInput, httpx.PutLimitResultDTO] {
	return writeLimitHandler(src, domain.LimitWriteChange)
}

// writeLimitHandler returns the stored barrier under the key of its kind.
func writeLimitHandler(
	src frameworkmcp.Source, write domain.LimitWrite,
) frameworkmcp.ToolBody[limitWriteInput, httpx.PutLimitResultDTO] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in limitWriteInput,
	) (string, httpx.PutLimitResultDTO, error) {
		var verb string
		switch write {
		case domain.LimitWriteAdd:
			verb = "added"
		case domain.LimitWriteChange:
			verb = "set"
		default:
			return "", httpx.PutLimitResultDTO{}, fmt.Errorf("mcptools: unsupported limit write intent %q", write)
		}
		var put func(
			context.Context, frameworkmcp.Source, limitWriteInput, domain.LimitWrite,
		) (httpx.PutLimitResultDTO, error)
		switch in.Policy {
		case domain.PolicyRateLimit:
			put = putRateLimit
		case domain.PolicyOrderSizeLimit:
			put = putOrderSizeLimit
		case domain.PolicyPositionLimit:
			put = putPositionLimit
		case domain.PolicySpotFundsPnlBoundsKillSwitch,
			domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
			domain.PolicySpotFundsTotalPnlBoundsKillSwitch:
			put = putSpotFundsPnlBoundsLimit
		case domain.PolicySpotFundsLimitMode:
			if write == domain.LimitWriteChange {
				put = putSpotFundsLimitMode
			}
		}
		if put == nil {
			allowed := []string{
				domain.PolicyRateLimit, domain.PolicyOrderSizeLimit, domain.PolicyPositionLimit,
				domain.PolicySpotFundsPnlBoundsKillSwitch,
				domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
				domain.PolicySpotFundsTotalPnlBoundsKillSwitch,
			}
			if write == domain.LimitWriteChange {
				allowed = append(allowed, domain.PolicySpotFundsLimitMode)
			}
			for i, policy := range allowed {
				allowed[i] = fmt.Sprintf("%q", policy)
			}
			return "", httpx.PutLimitResultDTO{}, toolError(domain.NewValidationError(
				"/policy", "unsupported_value", fmt.Sprintf(
					"policy %q must be %s or %s", in.Policy,
					strings.Join(allowed[:len(allowed)-1], ", "), allowed[len(allowed)-1],
				),
			))
		}
		if err := in.refuseForeignFields(); err != nil {
			return "", httpx.PutLimitResultDTO{}, toolError(err)
		}
		out, err := put(ctx, src, in, write)
		if err != nil {
			return "", httpx.PutLimitResultDTO{}, toolError(err)
		}
		return fmt.Sprintf("%s %s %s", in.Policy, in.Scope, verb), out, nil
	}
}

func putRateLimit(
	ctx context.Context, src frameworkmcp.Source, in limitWriteInput, write domain.LimitWrite,
) (httpx.PutLimitResultDTO, error) {
	limit, err := httpx.FromRateLimitDTO(httpx.RateLimitDTO{
		Scope: in.Scope, Account: in.Account, Asset: in.Asset,
		WindowMs: in.WindowMs, MaxOrders: in.MaxOrders,
	})
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	err = src.PutRateLimit(ctx, limit, write, missingAccountPolicy(limit.Account, in.MissingAccount))
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	stored, err := httpx.PersistedRateLimit(ctx, src, limit)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	dto := httpx.ToRateLimitDTO(stored)
	return httpx.PutLimitResultDTO{RateLimit: &dto}, nil
}

func putOrderSizeLimit(
	ctx context.Context, src frameworkmcp.Source, in limitWriteInput, write domain.LimitWrite,
) (httpx.PutLimitResultDTO, error) {
	limit := httpx.FromOrderSizeLimitDTO(httpx.OrderSizeLimitDTO{
		Scope: in.Scope, Account: in.Account, Asset: in.Asset,
		MaxQuantity: in.MaxQuantity, MaxNotional: in.MaxNotional,
	})
	err := src.PutOrderSizeLimit(ctx, limit, write, missingAccountPolicy(limit.Account, in.MissingAccount))
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	stored, err := httpx.PersistedOrderSizeLimit(ctx, src, limit)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	dto := httpx.ToOrderSizeLimitDTO(stored)
	return httpx.PutLimitResultDTO{OrderSizeLimit: &dto}, nil
}

func putPositionLimit(
	ctx context.Context, src frameworkmcp.Source, in limitWriteInput, write domain.LimitWrite,
) (httpx.PutLimitResultDTO, error) {
	limit := httpx.FromPositionLimitDTO(httpx.PositionLimitDTO{
		Scope: in.Scope, Account: in.Account, Asset: in.Asset,
		MaxPosition: in.MaxPosition,
	})
	err := src.PutPositionLimit(ctx, limit, write, missingAccountPolicy(limit.Account, in.MissingAccount))
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	stored, err := httpx.PersistedPositionLimit(ctx, src, limit)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	dto := httpx.ToPositionLimitDTO(stored)
	return httpx.PutLimitResultDTO{PositionLimit: &dto}, nil
}

func putSpotFundsPnlBoundsLimit(
	ctx context.Context, src frameworkmcp.Source, in limitWriteInput, write domain.LimitWrite,
) (httpx.PutLimitResultDTO, error) {
	axis, ok := domain.SpotFundsPnlAxisOf(in.Policy)
	if !ok {
		return httpx.PutLimitResultDTO{}, fmt.Errorf(
			"mcptools: policy %q bounds no Spot Funds P&L axis", in.Policy,
		)
	}
	limit := httpx.FromSpotFundsPnlBoundsLimitDTO(axis, httpx.SpotFundsPnlBoundsLimitDTO{
		Scope: in.Scope, Account: in.Account, AccountGroup: in.AccountGroup,
		Currency: in.Currency, LowerBound: in.LowerBound, UpperBound: in.UpperBound,
	})
	err := src.PutSpotFundsPnlBoundsLimit(
		ctx, limit, write, missingAccountPolicy(limit.Account, in.MissingAccount),
	)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	stored, err := httpx.PersistedSpotFundsPnlBoundsLimit(ctx, src, limit)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	return httpx.ToPutSpotFundsPnlBoundsLimitResultDTO(stored)
}

// putSpotFundsLimitMode sets the Spot Funds limit-mode meta-policy. It has no
// account axis, so missingAccount is ignored, as on a global P&L bound.
func putSpotFundsLimitMode(
	ctx context.Context, src frameworkmcp.Source, in limitWriteInput, _ domain.LimitWrite,
) (httpx.PutLimitResultDTO, error) {
	if in.Mode == string(domain.SpotFundsLimitModeReviewWithHold) ||
		in.Mode == string(domain.SpotFundsLimitModeReviewWithoutHold) {
		return httpx.PutLimitResultDTO{}, domain.NewValidationError(
			"/mode", "unsupported_value",
			"review values of the fund-control setting are set through REST or the panel",
		)
	}
	limit := httpx.FromSpotFundsLimitModeLimitDTO(httpx.SpotFundsLimitModeLimitDTO{
		Scope: in.Scope, Mode: in.Mode,
	})
	if err := src.PutSpotFundsLimitMode(ctx, limit); err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	stored, err := httpx.PersistedSpotFundsLimitMode(ctx, src)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	dto := httpx.ToSpotFundsLimitModeLimitDTO(stored)
	return httpx.PutLimitResultDTO{SpotFundsLimitMode: &dto}, nil
}
