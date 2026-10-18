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

// The limit tool maps its flat JSON arguments onto the limit put REST makes
// for the policy they name: the fields are that put's body and reach the core
// verbatim, missingAccount only with a named account, as REST reads its query
// parameter, and the core validates every value and the missing-account
// policy and refuses an agent anything that loosens risk. The tool itself
// refuses only a policy it does not set and a field of another policy.

const setLimitToolName = "set_limit"

const setLimitToolDescription = "Create a risk barrier, or make the one " +
	"stored at the same address stricter, and return it as stored. policy " +
	"names the kind - rate_limit, order_size_limit or " +
	"spot_funds_pnl_bounds_kill_switch - and the other arguments are that " +
	"kind's fields; a field of another kind is refused with validation. " +
	"Anything that loosens risk is refused with loosening_refused and changes " +
	"nothing. A barrier that replaces the one at its address must be no " +
	"looser: for rate_limit no more orders and, on the broker and asset " +
	"scopes, the same window, on the account and account_asset scopes a " +
	"window no shorter; for order_size_limit every current cap kept and none " +
	"raised; for spot_funds_pnl_bounds_kill_switch the same currency, every " +
	"current bound kept, the lower bound no lower and the upper bound no " +
	"higher. A new account_underlying_asset or account_settlement_asset cap " +
	"must be no higher than the underlying_asset or settlement_asset cap on " +
	"the same asset that it overrides, and a new account_group or account " +
	"bound must be in the currency of, and no looser than, the bound it takes " +
	"over from: for a group the global bound, for an account the bound that " +
	"applies to it now. The first order-size barrier is refused while any " +
	"rate-limit barrier is configured. No barrier can be removed. Mutates " +
	"Officer control-plane config; protected and disabled by default."

// setLimitInput is the REST body of the named policy's put, flattened next to
// the policy, with REST's missingAccount query value.
type setLimitInput struct {
	Policy         string `json:"policy" jsonschema:"Barrier kind: rate_limit, order_size_limit or spot_funds_pnl_bounds_kill_switch"`
	Scope          string `json:"scope" jsonschema:"Barrier scope: broker, asset, account or account_asset for rate_limit; broker, underlying_asset, settlement_asset, account_underlying_asset or account_settlement_asset for order_size_limit; global, account_group or account for spot_funds_pnl_bounds_kill_switch"`
	Account        string `json:"account,omitempty" jsonschema:"Account code, for a scope with an account axis; omitted otherwise"`
	Asset          string `json:"asset,omitempty" jsonschema:"rate_limit and order_size_limit: asset code, for a scope with an asset axis - the underlying asset for a quantity cap, the settlement asset for a notional cap; omitted otherwise"`
	AccountGroup   string `json:"accountGroup,omitempty" jsonschema:"spot_funds_pnl_bounds_kill_switch: account group code, for the account_group scope"`
	WindowMs       int64  `json:"windowMs,omitempty" jsonschema:"rate_limit: window in milliseconds, fixed on the broker and asset scopes and rolling on the account and account_asset scopes"`
	MaxOrders      uint64 `json:"maxOrders,omitempty" jsonschema:"rate_limit: most orders admitted within the window"`
	MaxQuantity    string `json:"maxQuantity,omitempty" jsonschema:"order_size_limit: per-order quantity cap as an exact decimal string; a broker barrier carries either cap or both, the underlying-asset scopes this one only"`
	MaxNotional    string `json:"maxNotional,omitempty" jsonschema:"order_size_limit: per-order notional cap as an exact decimal string; the settlement-asset scopes carry this one only"`
	Currency       string `json:"currency,omitempty" jsonschema:"spot_funds_pnl_bounds_kill_switch: code of an existing asset that denominates both bounds"`
	LowerBound     string `json:"lowerBound,omitempty" jsonschema:"spot_funds_pnl_bounds_kill_switch: lower P&L bound as an exact decimal string in plain notation, without an exponent"`
	UpperBound     string `json:"upperBound,omitempty" jsonschema:"spot_funds_pnl_bounds_kill_switch: upper P&L bound, written as lowerBound is; at least one bound is set"`
	MissingAccount string `json:"missingAccount,omitempty" jsonschema:"Needed when account is named: create or reject; for an account that does not exist yet, create is refused for an agent with loosening_refused, and reject fails the call instead; ignored otherwise"`
	// keys are the argument names the call sent, as it spelled them.
	keys []string
}

// UnmarshalJSON decodes the arguments as the SDK decodes those of every tool,
// refusing an undeclared one, and keeps the names the call sent: an argument
// sent empty, zero or null decodes as one omitted, and refuseForeignFields
// reads the names to refuse it all the same.
func (in *setLimitInput) UnmarshalJSON(data []byte) error {
	type setLimitArguments setLimitInput
	var args setLimitArguments
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return err
	}
	var sent map[string]json.RawMessage
	if err := json.Unmarshal(data, &sent); err != nil {
		return err
	}
	*in = setLimitInput(args)
	in.keys = slices.Collect(maps.Keys(sent))
	return nil
}

// sent reports whether the call sent the argument name, matching its key
// ignoring case as encoding/json matches a key to a field.
func (in setLimitInput) sent(name string) bool {
	return slices.ContainsFunc(in.keys, func(key string) bool {
		return strings.EqualFold(key, name)
	})
}

// refuseForeignFields refuses an argument of another policy than the one in
// names, sent with any value, as no body of that policy carries it.
func (in setLimitInput) refuseForeignFields() error {
	rate := domain.PolicyRateLimit
	size := domain.PolicyOrderSizeLimit
	bounds := domain.PolicySpotFundsPnlBoundsKillSwitch
	for _, field := range []struct {
		name     string
		policies []string
	}{
		{"asset", []string{rate, size}},
		{"accountGroup", []string{bounds}},
		{"windowMs", []string{rate}},
		{"maxOrders", []string{rate}},
		{"maxQuantity", []string{size}},
		{"maxNotional", []string{size}},
		{"currency", []string{bounds}},
		{"lowerBound", []string{bounds}},
		{"upperBound", []string{bounds}},
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

// setLimitHandler makes the put of the named policy as REST does and answers
// REST's body: the barrier read back as stored, under the key of its kind.
func setLimitHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[setLimitInput, httpx.PutLimitResultDTO] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in setLimitInput,
	) (string, httpx.PutLimitResultDTO, error) {
		var put func(
			context.Context, frameworkmcp.Source, setLimitInput,
		) (httpx.PutLimitResultDTO, error)
		switch in.Policy {
		case domain.PolicyRateLimit:
			put = putRateLimit
		case domain.PolicyOrderSizeLimit:
			put = putOrderSizeLimit
		case domain.PolicySpotFundsPnlBoundsKillSwitch:
			put = putSpotFundsPnlBoundsLimit
		default:
			return "", httpx.PutLimitResultDTO{}, toolError(domain.NewValidationError(
				"/policy", "unsupported_value", fmt.Sprintf(
					"policy %q must be %q, %q or %q", in.Policy, domain.PolicyRateLimit,
					domain.PolicyOrderSizeLimit, domain.PolicySpotFundsPnlBoundsKillSwitch,
				),
			))
		}
		if err := in.refuseForeignFields(); err != nil {
			return "", httpx.PutLimitResultDTO{}, toolError(err)
		}
		out, err := put(ctx, src, in)
		if err != nil {
			return "", httpx.PutLimitResultDTO{}, toolError(err)
		}
		return fmt.Sprintf("%s %s set", in.Policy, in.Scope), out, nil
	}
}

func putRateLimit(
	ctx context.Context, src frameworkmcp.Source, in setLimitInput,
) (httpx.PutLimitResultDTO, error) {
	limit, err := httpx.FromRateLimitDTO(httpx.RateLimitDTO{
		Scope: in.Scope, Account: in.Account, Asset: in.Asset,
		WindowMs: in.WindowMs, MaxOrders: in.MaxOrders,
	})
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	err = src.PutRateLimit(ctx, limit, missingAccountPolicy(limit.Account, in.MissingAccount))
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
	ctx context.Context, src frameworkmcp.Source, in setLimitInput,
) (httpx.PutLimitResultDTO, error) {
	limit := httpx.FromOrderSizeLimitDTO(httpx.OrderSizeLimitDTO{
		Scope: in.Scope, Account: in.Account, Asset: in.Asset,
		MaxQuantity: in.MaxQuantity, MaxNotional: in.MaxNotional,
	})
	err := src.PutOrderSizeLimit(ctx, limit, missingAccountPolicy(limit.Account, in.MissingAccount))
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

func putSpotFundsPnlBoundsLimit(
	ctx context.Context, src frameworkmcp.Source, in setLimitInput,
) (httpx.PutLimitResultDTO, error) {
	limit := httpx.FromSpotFundsPnlBoundsLimitDTO(httpx.SpotFundsPnlBoundsLimitDTO{
		Scope: in.Scope, Account: in.Account, AccountGroup: in.AccountGroup,
		Currency: in.Currency, LowerBound: in.LowerBound, UpperBound: in.UpperBound,
	})
	err := src.PutSpotFundsPnlBoundsLimit(
		ctx, limit, missingAccountPolicy(limit.Account, in.MissingAccount),
	)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	stored, err := httpx.PersistedSpotFundsPnlBoundsLimit(ctx, src, limit)
	if err != nil {
		return httpx.PutLimitResultDTO{}, err
	}
	dto := httpx.ToSpotFundsPnlBoundsLimitDTO(stored)
	return httpx.PutLimitResultDTO{SpotFundsPnlBoundsLimit: &dto}, nil
}
