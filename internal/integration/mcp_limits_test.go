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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/accountmode"
)

// limitWriteDriver calls add_limit or update_limit and checks the barriers of
// one kind that the service stores against what each call sends.
type limitWriteDriver[Limit comparable] struct {
	t   *testing.T
	src frameworkmcp.Source
	// stored reads every barrier of the kind the service stores.
	stored func() []Limit
	// body is the body REST answers for a barrier it stored.
	body func(Limit) map[string]any
}

// accepts requires the tool to answer text and the body
// REST answers for want, which the service stores as the only barrier of the
// kind.
func (d limitWriteDriver[Limit]) accepts(tool string, in map[string]any, text string, want Limit) {
	d.t.Helper()
	res := callTool[map[string]any](d.t, d.src, tool, in)
	if res.IsError {
		d.t.Fatalf("%s %v: %s", tool, in, textContent(res.Content))
	}
	if got := summaryText(res.Content); got != text {
		d.t.Fatalf("%s %v text = %q, want %q", tool, in, got, text)
	}
	if got := d.stored(); !slices.Equal(got, []Limit{want}) {
		d.t.Fatalf("%s %v stored %+v, want %+v", tool, in, got, want)
	}
	assertResultBody(d.t, res.Wire, d.body(want))
}

// refuses requires a tool error opening with code
// that leaves the barriers of the kind as kept.
func (d limitWriteDriver[Limit]) refuses(tool string, in map[string]any, code string, kept ...Limit) {
	d.t.Helper()
	res := callTool[map[string]any](d.t, d.src, tool, in)
	if text := textContent(res.Content); !res.IsError || !strings.HasPrefix(text, code+": ") {
		d.t.Fatalf("%s %v = %q (tool error %t), want %s", tool, in, text, res.IsError, code)
	}
	if got := d.stored(); !slices.Equal(got, kept) {
		d.t.Fatalf("%s %v left %+v stored, want %+v", tool, in, got, kept)
	}
}

// storedLimits reads every barrier the service stores.
func storedLimits(t *testing.T, service *backend.Service) node.AccountLimits {
	t.Helper()
	limits, err := service.ListLimits(systemCtx(), "")
	if err != nil {
		t.Fatalf("list limits: %v", err)
	}
	return limits
}

func rateLimitDriver(
	t *testing.T, service *backend.Service, src frameworkmcp.Source,
) limitWriteDriver[domain.LimitRate] {
	return limitWriteDriver[domain.LimitRate]{
		t: t, src: src,
		stored: func() []domain.LimitRate { return storedLimits(t, service).RateLimits },
		body: func(limit domain.LimitRate) map[string]any {
			return map[string]any{"rateLimit": httpx.ToRateLimitDTO(limit)}
		},
	}
}

func orderSizeLimitDriver(
	t *testing.T, service *backend.Service, src frameworkmcp.Source,
) limitWriteDriver[domain.LimitOrderSize] {
	return limitWriteDriver[domain.LimitOrderSize]{
		t: t, src: src,
		stored: func() []domain.LimitOrderSize { return storedLimits(t, service).OrderSizeLimits },
		body: func(limit domain.LimitOrderSize) map[string]any {
			return map[string]any{"orderSizeLimit": httpx.ToOrderSizeLimitDTO(limit)}
		},
	}
}

func positionLimitDriver(
	t *testing.T, service *backend.Service, src frameworkmcp.Source,
) limitWriteDriver[domain.LimitPosition] {
	return limitWriteDriver[domain.LimitPosition]{
		t: t, src: src,
		stored: func() []domain.LimitPosition { return storedLimits(t, service).PositionLimits },
		body: func(limit domain.LimitPosition) map[string]any {
			return map[string]any{"positionLimit": httpx.ToPositionLimitDTO(limit)}
		},
	}
}

// spotFundsPnlBoundsDriver drives the P&L-bound kill switch of axis: it sees
// only the stored bounds of that axis, and REST's body answers them under that
// axis's key.
func spotFundsPnlBoundsDriver(
	t *testing.T, service *backend.Service, src frameworkmcp.Source, axis domain.SpotFundsPnlAxis,
) limitWriteDriver[domain.LimitSpotFundsPnlBounds] {
	key := map[domain.SpotFundsPnlAxis]string{
		domain.SpotFundsPnlAxisRealized:   "spotFundsPnlBoundsLimit",
		domain.SpotFundsPnlAxisUnrealized: "spotFundsUnrealizedPnlBoundsLimit",
		domain.SpotFundsPnlAxisTotal:      "spotFundsTotalPnlBoundsLimit",
	}[axis]
	if key == "" {
		t.Fatalf("no put result key for P&L axis %q", axis)
	}
	return limitWriteDriver[domain.LimitSpotFundsPnlBounds]{
		t: t, src: src,
		stored: func() []domain.LimitSpotFundsPnlBounds {
			var stored []domain.LimitSpotFundsPnlBounds
			for _, limit := range storedLimits(t, service).SpotFundsPnlBoundsLimits {
				if limit.Axis == axis {
					stored = append(stored, limit)
				}
			}
			return stored
		},
		body: func(limit domain.LimitSpotFundsPnlBounds) map[string]any {
			return map[string]any{key: httpx.ToSpotFundsPnlBoundsLimitDTO(limit)}
		},
	}
}

// requireNoLimits fails the test unless the service stores no barrier.
func requireNoLimits(t *testing.T, service *backend.Service) {
	t.Helper()
	limits := storedLimits(t, service)
	if len(limits.RateLimits)+len(limits.OrderSizeLimits)+len(limits.PositionLimits)+len(limits.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("stored barriers %+v, want none", limits)
	}
}

// getStoredLimitsThroughMCP returns the barriers get_limits reports.
func getStoredLimitsThroughMCP(t *testing.T, src frameworkmcp.Source) httpx.AccountLimitsDTO {
	t.Helper()
	listed := callTool[struct {
		Limits httpx.AccountLimitsDTO `json:"limits"`
	}](t, src, "get_limits", map[string]any{})
	if listed.IsError {
		t.Fatalf("get_limits: %s", textContent(listed.Content))
	}
	return listed.StructuredContent.Limits
}

func TestMCPOrderSizeLimitModes(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	review := domain.LimitOrderSize{
		Mode: domain.RuleModeReviewWithoutHold, Scope: domain.ScopeBroker, MaxQuantity: "20",
	}
	if err := service.PutOrderSizeLimit(systemCtx(), review, domain.LimitWriteAdd, domain.MissingAccountReject); err != nil {
		t.Fatal(err)
	}
	driver := orderSizeLimitDriver(t, service, src)
	for _, tool := range []string{"add_limit", "update_limit"} {
		for _, mode := range []string{"review_without_hold", "reject", ""} {
			driver.refuses(tool, map[string]any{
				"policy": "order_size_limit", "scope": "broker", "maxQuantity": "10", "mode": mode,
			}, "validation", review)
		}
	}
	added := callTool[httpx.PutLimitResultDTO](t, src, "add_limit", map[string]any{
		"policy": "order_size_limit", "scope": "broker", "maxQuantity": "10",
	})
	if added.IsError || added.StructuredContent.OrderSizeLimit == nil || added.StructuredContent.OrderSizeLimit.Mode != "reject" {
		t.Fatalf("add reject beside review = %+v: %s", added.StructuredContent, textContent(added.Content))
	}
	changed := callTool[httpx.PutLimitResultDTO](t, src, "update_limit", map[string]any{
		"policy": "order_size_limit", "scope": "broker", "maxQuantity": "5",
	})
	if changed.IsError || changed.StructuredContent.OrderSizeLimit == nil || changed.StructuredContent.OrderSizeLimit.Mode != "reject" {
		t.Fatalf("update reject beside review = %+v: %s", changed.StructuredContent, textContent(changed.Content))
	}
	listed := getStoredLimitsThroughMCP(t, src).OrderSizeLimits
	if len(listed) != 2 {
		t.Fatalf("get_limits = %+v, want both modes", listed)
	}
	seen := map[string]string{}
	for _, limit := range listed {
		seen[limit.Mode] = limit.MaxQuantity
	}
	if seen["reject"] != "5" || seen["review_without_hold"] != "20" {
		t.Fatalf("get_limits modes and caps = %v", seen)
	}
	stored := storedLimits(t, service).OrderSizeLimits
	if len(stored) != 2 || !slices.Contains(stored, review) {
		t.Fatalf("MCP writes changed review barrier: %+v", stored)
	}
}

// TestUpdateLimitRateOnControlPlane drives add_limit and update_limit for
// rate_limit through the control-plane Source into a local service, as an
// agent: a new barrier and a stricter one are stored exactly as sent and
// answered with REST's body, which get_limits reads back in the same shape.
// More orders and another window on the broker scope are refused with
// loosening_refused, a field of another policy with validation, sent empty as
// well, and each leaves the barrier as it was. The first order-size barrier is
// refused with loosening_refused while the rate barrier exists.
func TestUpdateLimitRateOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	rate := rateLimitDriver(t, service, src)

	first := domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, Window: time.Minute, MaxOrders: 10}
	rate.accepts("add_limit", map[string]any{
		"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 10,
	}, "rate_limit broker added", first)
	orderSizeLimitDriver(t, service, src).refuses("add_limit", map[string]any{
		"policy": "order_size_limit", "scope": "broker", "maxQuantity": "1",
	}, "loosening_refused")
	rate.refuses("add_limit", map[string]any{
		"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 9,
	}, "limit_exists", first)
	listed := getStoredLimitsThroughMCP(t, src)
	if got, want := listed.RateLimits, []httpx.RateLimitDTO{httpx.ToRateLimitDTO(first)}; !slices.Equal(got, want) {
		t.Fatalf("get_limits after duplicate add = %+v, want %+v", got, want)
	}
	stricter := domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, Window: time.Minute, MaxOrders: 5}
	rate.accepts("update_limit", map[string]any{
		"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 5,
	}, "rate_limit broker set", stricter)

	for _, in := range []map[string]any{
		// More orders.
		{"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 6},
		// A broker barrier counts in a fixed window, so a longer one loosens
		// too.
		{"policy": "rate_limit", "scope": "broker", "windowMs": 120000, "maxOrders": 5},
	} {
		rate.refuses("update_limit", in, "loosening_refused", stricter)
	}
	// An empty field decodes as an omitted one, and is refused all the same.
	for _, quantity := range []string{"1", ""} {
		rate.refuses("update_limit", map[string]any{
			"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 4,
			"maxQuantity": quantity,
		}, "validation", stricter)
	}

	wire := callToolOK(t, src, "get_limits", map[string]any{})
	assertResultBody(t, wire, map[string]any{"limits": map[string]any{
		"rateLimits":                         []httpx.RateLimitDTO{httpx.ToRateLimitDTO(stricter)},
		"orderSizeLimits":                    []any{},
		"positionLimits":                     []any{},
		"spotFundsPnlBoundsLimits":           []any{},
		"spotFundsUnrealizedPnlBoundsLimits": []any{},
		"spotFundsTotalPnlBoundsLimits":      []any{},
		"spotFundsLimitModes":                []httpx.SpotFundsLimitModeLimitDTO{httpx.ToSpotFundsLimitModeLimitDTO(domain.LimitSpotFundsLimitMode{Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeEnforce})},
	}})
}

// TestMCPGetLimitsCountingRuleModes verifies that get_limits lists reject and
// review-with-hold rows for rate, position, and P&L limits.
func TestMCPGetLimitsCountingRuleModes(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	ctx := systemCtx()
	if _, err := service.CreateAccount(ctx, domain.Account{Code: "desk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAsset(ctx, domain.Asset{Code: "USD"}); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithHold} {
		if err := service.PutRateLimit(ctx, domain.LimitRate{
			Mode: mode, Scope: domain.ScopeBroker, Window: time.Minute, MaxOrders: 10,
		}, domain.LimitWriteAdd, domain.MissingAccountReject); err != nil {
			t.Fatal(err)
		}
		if err := service.PutPositionLimit(ctx, domain.LimitPosition{
			Mode: mode, Scope: domain.ScopeAccountAsset, Account: "desk", Asset: "USD", MaxPosition: "10",
		}, domain.LimitWriteAdd, domain.MissingAccountReject); err != nil {
			t.Fatal(err)
		}
		if err := service.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{
			Mode: mode, Axis: domain.SpotFundsPnlAxisRealized, Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10",
		}, domain.LimitWriteAdd, domain.MissingAccountReject); err != nil {
			t.Fatal(err)
		}
	}
	limits := getStoredLimitsThroughMCP(t, src)
	var rateModes, positionModes, pnlModes []string
	for _, limit := range limits.RateLimits {
		rateModes = append(rateModes, limit.Mode)
	}
	for _, limit := range limits.PositionLimits {
		positionModes = append(positionModes, limit.Mode)
	}
	for _, limit := range limits.SpotFundsPnlBoundsLimits {
		pnlModes = append(pnlModes, limit.Mode)
	}
	for _, tc := range []struct {
		name  string
		modes []string
	}{{"rate", rateModes}, {"position", positionModes}, {"P&L", pnlModes}} {
		slices.Sort(tc.modes)
		if !slices.Equal(tc.modes, []string{"reject", "review_with_hold"}) {
			t.Errorf("get_limits %s modes = %v, want reject and review_with_hold", tc.name, tc.modes)
		}
	}
}

func TestUpdateLimitRefusesAbsentKey(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	rateLimitDriver(t, service, src).refuses("update_limit", map[string]any{
		"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 10,
	}, "not_found")
	requireNoLimits(t, service)
}

func TestAddLimitRefusesSpotFundsMode(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	mode := spotFundsLimitModeDriver(t, service, src)
	for _, tc := range []struct {
		in   map[string]any
		want string
	}{
		{spotFundsLimitModeIn("enforce"), `validation: policy "spot_funds_limit_mode" must be`},
		{
			map[string]any{
				"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 10,
				"mode": "enforce",
			},
			"validation: mode is not a field of policy rate_limit",
		},
	} {
		res := callTool[map[string]any](t, src, "add_limit", tc.in)
		if text := textContent(res.Content); !res.IsError || !strings.HasPrefix(text, tc.want) {
			t.Fatalf("add_limit %v = %q (tool error %t), want %q", tc.in, text, res.IsError, tc.want)
		}
		if got := mode.stored(); len(got) != 0 {
			t.Fatalf("add_limit %v left a stored mode %+v", tc.in, got)
		}
		requireNoLimits(t, service)
	}
}

// TestUpdateLimitOrderSizeOnControlPlane drives add_limit and update_limit with
// the order_size_limit policy through the control-plane Source into a local
// service, as an agent: a new barrier and a stricter one are stored exactly as
// sent, digits float64 cannot hold included, and answered with REST's body. A
// cap raised by the last of those digits and a dropped cap are refused with
// loosening_refused, a field of another policy with validation, sent as zero
// as well, and each leaves the barrier as it was. An account cap added above
// the asset cap it overrides is refused with loosening_refused.
func TestUpdateLimitOrderSizeOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	size := orderSizeLimitDriver(t, service, src)

	size.accepts("add_limit", map[string]any{
		"policy": "order_size_limit", "scope": "broker",
		"maxQuantity": "10.0000000000000000001", "maxNotional": "1000",
	}, "order_size_limit broker added", domain.LimitOrderSize{
		Mode:  domain.RuleModeReject,
		Scope: domain.ScopeBroker, MaxQuantity: "10.0000000000000000001", MaxNotional: "1000",
	})
	if limits := storedLimits(t, service).OrderSizeLimits; len(limits) != 1 || limits[0].Mode != domain.RuleModeReject {
		t.Fatalf("MCP order-size write stored %+v, want reject mode", limits)
	}
	stricter := domain.LimitOrderSize{
		Mode:  domain.RuleModeReject,
		Scope: domain.ScopeBroker, MaxQuantity: "5.0000000000000000001", MaxNotional: "1000",
	}
	size.accepts("update_limit", map[string]any{
		"policy": "order_size_limit", "scope": "broker",
		"maxQuantity": "5.0000000000000000001", "maxNotional": "1000",
	}, "order_size_limit broker set", stricter)

	for _, in := range []map[string]any{
		// The quantity cap raised by 1e-19.
		{
			"policy": "order_size_limit", "scope": "broker",
			"maxQuantity": "5.0000000000000000002", "maxNotional": "1000",
		},
		// The notional cap dropped.
		{"policy": "order_size_limit", "scope": "broker", "maxQuantity": "5"},
	} {
		size.refuses("update_limit", in, "loosening_refused", stricter)
	}
	// A zero field decodes as an omitted one, and is refused all the same.
	for _, window := range []int{1000, 0} {
		size.refuses("update_limit", map[string]any{
			"policy": "order_size_limit", "scope": "broker",
			"maxQuantity": "4", "maxNotional": "1000", "windowMs": window,
		}, "validation", stricter)
	}

	t.Run("account cap above asset cap", func(t *testing.T) {
		service, src := controlPlaneTools(t)
		if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "USD"}); err != nil {
			t.Fatalf("create asset USD: %v", err)
		}
		if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "acc-1", Currency: "USD"}); err != nil {
			t.Fatalf("create account: %v", err)
		}
		size := orderSizeLimitDriver(t, service, src)
		assetCap := domain.LimitOrderSize{
			Mode:  domain.RuleModeReject,
			Scope: domain.ScopeUnderlyingAsset, Asset: "USD", MaxQuantity: "2",
		}
		size.accepts("add_limit", map[string]any{
			"policy": "order_size_limit", "scope": "underlying_asset",
			"asset": "USD", "maxQuantity": "2",
		}, "order_size_limit underlying_asset added", assetCap)
		size.refuses("add_limit", map[string]any{
			"policy": "order_size_limit", "scope": "account_underlying_asset",
			"account": "acc-1", "asset": "USD", "maxQuantity": "3", "missingAccount": "reject",
		}, "loosening_refused", assetCap)
	})
}

func TestUpdateLimitPositionOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "desk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "ETH"}); err != nil {
		t.Fatal(err)
	}
	position := positionLimitDriver(t, service, src)
	for _, step := range []struct{ tool, verb, ceiling string }{
		{"add_limit", "added", "10.0000000000000000001"},
		{"update_limit", "set", "5.0000000000000000001"},
		{"update_limit", "set", "0"},
	} {
		stored := domain.LimitPosition{Mode: domain.RuleModeReject,
			Scope: domain.ScopeAccountAsset, Account: "desk", Asset: "ETH", MaxPosition: step.ceiling,
		}
		position.accepts(step.tool, map[string]any{
			"policy": "position_limit", "scope": "account_asset", "account": "desk", "asset": "ETH",
			"maxPosition": step.ceiling, "missingAccount": "reject",
		}, "position_limit account_asset "+step.verb, stored)
		assertResultBody(t, callToolOK(t, src, "get_limits", map[string]any{"account": "desk"}),
			map[string]any{"limits": httpx.ToAccountLimitsDTO(node.AccountLimits{
				PositionLimits: []domain.LimitPosition{stored},
			})})
		state := callTool[struct {
			Limits httpx.AccountLimitsDTO `json:"limits"`
		}](t, src, "get_account_state", map[string]any{"account": "desk"})
		if state.IsError || !slices.Equal(state.StructuredContent.Limits.PositionLimits, []httpx.PositionLimitDTO{httpx.ToPositionLimitDTO(stored)}) {
			t.Fatalf("get_account_state position limits = %+v (error %t)", state.StructuredContent.Limits.PositionLimits, state.IsError)
		}
	}
	zero := domain.LimitPosition{Mode: domain.RuleModeReject, Scope: domain.ScopeAccountAsset, Account: "desk", Asset: "ETH", MaxPosition: "0"}
	for _, ceiling := range []string{"-1", "not-a-decimal", ""} {
		position.refuses("update_limit", map[string]any{
			"policy": "position_limit", "scope": "account_asset", "account": "desk", "asset": "ETH",
			"maxPosition": ceiling, "missingAccount": "reject",
		}, "validation", zero)
	}
}

func TestPositionLimitReadSummaries(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "desk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "ETH"}); err != nil {
		t.Fatal(err)
	}
	if err := service.PutPositionLimit(systemCtx(), domain.LimitPosition{Mode: domain.RuleModeReject,
		Scope: domain.ScopeAccountAsset, Account: "desk", Asset: "ETH", MaxPosition: "1",
	}, domain.LimitWriteReplace, domain.MissingAccountReject); err != nil {
		t.Fatal(err)
	}
	for tool, want := range map[string]string{
		"get_limits": "1 limit(s)", "get_account_state": "account desk: 1 limit(s)",
	} {
		res := callTool[map[string]any](t, src, tool, map[string]any{"account": "desk"})
		if got := summaryText(res.Content); res.IsError || got != want {
			t.Errorf("%s summary = %q (error %t), want %q", tool, got, res.IsError, want)
		}
	}
}

func TestUpdateLimitPositionRefusesForeignFields(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "desk"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "ETH"}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []map[string]any{
		{"policy": "position_limit", "scope": "account_asset", "account": "desk", "asset": "ETH", "maxPosition": "1", "maxQuantity": "1", "missingAccount": "reject"},
		{"policy": "position_limit", "scope": "account_asset", "account": "desk", "asset": "ETH", "maxPosition": "1", "maxQuantity": "", "missingAccount": "reject"},
		{"policy": "order_size_limit", "scope": "broker", "maxQuantity": "1", "maxPosition": "1"},
		{"policy": "order_size_limit", "scope": "broker", "maxQuantity": "1", "maxPosition": ""},
	} {
		positionLimitDriver(t, service, src).refuses("update_limit", in, "validation")
		requireNoLimits(t, service)
	}
}

// TestUpdateLimitSpotFundsPnlBoundsOnControlPlane drives both limit writes for
// the spot_funds_pnl_bounds_kill_switch policy through the control-plane Source
// into a local service, as an agent: a new bound and a stricter one are stored
// exactly as sent and answered with REST's body. A lower bound lowered by
// 1e-19, a dropped bound and bounds in another currency are refused with
// loosening_refused, a field of another policy with validation, sent empty or
// null as well, and each leaves the bound as it was.
func TestUpdateLimitSpotFundsPnlBoundsOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	for _, code := range []string{"USD", "EUR"} {
		if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: code}); err != nil {
			t.Fatalf("create asset %s: %v", code, err)
		}
	}
	bounds := spotFundsPnlBoundsDriver(t, service, src, domain.SpotFundsPnlAxisRealized)
	global := func(currency, lower, upper string) map[string]any {
		in := map[string]any{
			"policy": "spot_funds_pnl_bounds_kill_switch", "scope": "global",
			"currency": currency, "lowerBound": lower,
		}
		if upper != "" {
			in["upperBound"] = upper
		}
		return in
	}
	const text = "spot_funds_pnl_bounds_kill_switch global set"

	first := domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
		Axis:  domain.SpotFundsPnlAxisRealized,
		Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-100", UpperBound: "100",
	}
	bounds.accepts("add_limit", global("USD", "-100", "100"), "spot_funds_pnl_bounds_kill_switch global added", first)
	bounds.refuses("add_limit", global("USD", "-75", "100"), "limit_exists", first)
	listed := getStoredLimitsThroughMCP(t, src)
	if got, want := listed.SpotFundsPnlBoundsLimits, []httpx.SpotFundsPnlBoundsLimitDTO{httpx.ToSpotFundsPnlBoundsLimitDTO(first)}; !slices.Equal(got, want) {
		t.Fatalf("get_limits after duplicate add = %+v, want %+v", got, want)
	}
	stricter := domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
		Axis:  domain.SpotFundsPnlAxisRealized,
		Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-50", UpperBound: "100",
	}
	bounds.accepts("update_limit", global("USD", "-50", "100"), text, stricter)

	for _, in := range []map[string]any{
		// The lower bound lowered by 1e-19.
		global("USD", "-50.0000000000000000001", "100"),
		// The upper bound dropped.
		global("USD", "-50", ""),
		// The bounds moved to another currency.
		global("EUR", "-50", "100"),
	} {
		bounds.refuses("update_limit", in, "loosening_refused", stricter)
	}
	// An empty or null field decodes as an omitted one, and is refused all the
	// same.
	for _, asset := range []any{"USD", "", nil} {
		foreign := global("USD", "-40", "100")
		foreign["asset"] = asset
		bounds.refuses("update_limit", foreign, "validation", stricter)
	}
}

// TestUpdateLimitUnrealizedAndTotalPnlBoundsOnControlPlane drives both writes
// for the unrealized and total P&L-bound kill switches through the control-plane
// Source, as an agent. Each axis is a policy of its own: a realized bound at
// the same address neither replaces nor constrains it. A new bound and a
// stricter one are stored as sent and answered under the axis's key; a lower
// bound lowered by 1e-19, a dropped bound and bounds in another currency are
// refused with loosening_refused, and an account bound looser than the global
// bound of its axis is refused too. Each refusal leaves the bounds of the axis,
// and the realized bound, as they were.
func TestUpdateLimitUnrealizedAndTotalPnlBoundsOnControlPlane(t *testing.T) {
	t.Parallel()
	for _, axis := range []domain.SpotFundsPnlAxis{
		domain.SpotFundsPnlAxisUnrealized, domain.SpotFundsPnlAxisTotal,
	} {
		t.Run(string(axis), func(t *testing.T) {
			t.Parallel()
			service, src := controlPlaneTools(t)
			for _, code := range []string{"USD", "EUR"} {
				if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: code}); err != nil {
					t.Fatalf("create asset %s: %v", code, err)
				}
			}
			if _, err := service.CreateAccount(systemCtx(), domain.Account{Code: "acc-1", Currency: "USD"}); err != nil {
				t.Fatalf("create account: %v", err)
			}
			realized := domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
				Axis:  domain.SpotFundsPnlAxisRealized,
				Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-10", UpperBound: "10",
			}
			spotFundsPnlBoundsDriver(t, service, src, domain.SpotFundsPnlAxisRealized).accepts(
				"add_limit", map[string]any{
					"policy": "spot_funds_pnl_bounds_kill_switch", "scope": "global",
					"currency": "USD", "lowerBound": "-10", "upperBound": "10",
				}, "spot_funds_pnl_bounds_kill_switch global added", realized)

			policy := string(axis.Policy())
			bounds := spotFundsPnlBoundsDriver(t, service, src, axis)
			in := func(scope, currency, lower, upper string) map[string]any {
				in := map[string]any{
					"policy": policy, "scope": scope, "currency": currency, "lowerBound": lower,
				}
				if scope == "account" {
					in["account"], in["missingAccount"] = "acc-1", "reject"
				}
				if upper != "" {
					in["upperBound"] = upper
				}
				return in
			}
			// Looser than the realized bound at the same address.
			bounds.accepts("add_limit", in("global", "USD", "-100", "100"), policy+" global added",
				domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
					Axis: axis, Scope: domain.ScopeGlobal,
					Currency: "USD", LowerBound: "-100", UpperBound: "100",
				})
			stricter := domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
				Axis: axis, Scope: domain.ScopeGlobal,
				Currency: "USD", LowerBound: "-50", UpperBound: "100",
			}
			bounds.accepts("update_limit", in("global", "USD", "-50", "100"), policy+" global set", stricter)

			for _, loose := range []map[string]any{
				// The lower bound lowered by 1e-19.
				in("global", "USD", "-50.0000000000000000001", "100"),
				// The upper bound dropped.
				in("global", "USD", "-50", ""),
				// The bounds moved to another currency.
				in("global", "EUR", "-50", "100"),
			} {
				bounds.refuses("update_limit", loose, "loosening_refused", stricter)
			}
			// An account bound looser than the global bound of its axis.
			bounds.refuses("add_limit", in("account", "USD", "-60", "100"), "loosening_refused", stricter)
			realizedStored := spotFundsPnlBoundsDriver(
				t, service, src, domain.SpotFundsPnlAxisRealized,
			).stored()
			if !slices.Equal(realizedStored, []domain.LimitSpotFundsPnlBounds{realized}) {
				t.Fatalf("realized bounds = %+v, want %+v", realizedStored, realized)
			}
		})
	}
}

// TestUpdateLimitRefusesUnknownPolicy calls update_limit with a policy it does
// not accept: the call is refused with validation naming the policy, and stores
// nothing.
func TestUpdateLimitRefusesUnknownPolicy(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	res := callTool[map[string]any](t, src, "update_limit", map[string]any{
		"policy": "kill_switch", "scope": "broker", "windowMs": 60000, "maxOrders": 10,
	})
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, `validation: policy "kill_switch"`) {
		t.Fatalf("update_limit of an unknown policy = %q, want validation naming it", text)
	}
	requireNoLimits(t, service)
}

// TestAddLimitRefusesUnrepresentableWindow calls add_limit with a rate window
// whose nanoseconds do not fit time.Duration: 2^58 + 60000 milliseconds, which
// a wrapping conversion would turn into a window of one minute. The call is
// refused with validation and stores nothing.
func TestAddLimitRefusesUnrepresentableWindow(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	rateLimitDriver(t, service, src).refuses("add_limit", map[string]any{
		"policy": "rate_limit", "scope": "broker",
		"windowMs": int64(288230376151771744), "maxOrders": 10,
	}, "validation")
}

// TestAddLimitRefusesBoundsTheEngineCannotParse drives add_limit with Spot
// Funds P&L bounds the engine cannot hold as written: a lower bound whose
// exponent a comparison would rescale by for minutes is refused with
// validation within a second, and one in exponent notation, which the engine's
// exact parser rejects, with validation too. Neither stores a bound or
// rebuilds the engine, which would reset every rate window; the first
// order-size barrier then shows that the build count sees a rebuild. It does
// not run in parallel, so the deadline measures the refusal and not the
// scheduler.
func TestAddLimitRefusesBoundsTheEngineCannotParse(t *testing.T) {
	var builds atomic.Int64
	build := engine.NewOpenPitEngineBuildFunc()
	target, _ := newLocalNodeBuiltBy(t, func(snap fwengine.Snapshot) (fwengine.Engine, error) {
		builds.Add(1)
		return build(snap)
	})
	service := newOfficerService(t, target, nil, nil)
	src := enabledControlPlaneSource{frameworkmcp.SourceFor(service)}
	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("create asset USD: %v", err)
	}
	built := builds.Load()
	bounds := spotFundsPnlBoundsDriver(t, service, src, domain.SpotFundsPnlAxisRealized)
	global := func(lower string) map[string]any {
		return map[string]any{
			"policy": "spot_funds_pnl_bounds_kill_switch", "scope": "global",
			"currency": "USD", "lowerBound": lower, "upperBound": "0",
		}
	}

	start := time.Now()
	bounds.refuses("add_limit", global("-1e-2000000000"), "validation")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the refusal took %s, want it within a second", elapsed)
	}
	bounds.refuses("add_limit", global("-1e2"), "validation")
	if got := builds.Load(); got != built {
		t.Fatalf("engine builds went from %d to %d, want the refusals to rebuild none", built, got)
	}

	// The count sees a rebuild: the first order-size barrier is installed by
	// one.
	orderSizeLimitDriver(t, service, src).accepts("add_limit", map[string]any{
		"policy": "order_size_limit", "scope": "broker", "maxQuantity": "1",
	}, "order_size_limit broker added", domain.LimitOrderSize{
		Mode:  domain.RuleModeReject,
		Scope: domain.ScopeBroker, MaxQuantity: "1",
	})
	if builds.Load() == built {
		t.Fatal("the first order-size barrier rebuilt the engine past the count")
	}
}

// TestLimitWritesDisabledChangeNothing drives both limit writes through the
// control-plane Source of a service as it ships, with both disabled, and
// after the operator switches it on, stores a barrier through it and switches
// it off again: while it is disabled the agent receives the panel's notice as
// a tool error, and the call changes no barrier.
func TestLimitWritesDisabledChangeNothing(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	target, _ := newLocalNode(t)
	service := newOfficerService(t, target, nil, nil)
	src := frameworkmcp.SourceFor(service)
	rate := rateLimitDriver(t, service, src)
	broker := func(maxOrders int) map[string]any {
		return map[string]any{
			"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": maxOrders,
		}
	}
	// disabled calls the tool with in and requires the panel's notice that
	// leaves the barriers as kept.
	disabled := func(tool string, in map[string]any, kept ...domain.LimitRate) {
		t.Helper()
		res := callTool[map[string]any](t, src, tool, in)
		requireDisabledNotice(t, res.IsError, res.Content, tool)
		if got := rate.stored(); !slices.Equal(got, kept) {
			t.Fatalf("disabled %s left %+v stored, want %+v", tool, got, kept)
		}
	}

	disabled("add_limit", broker(10))
	disabled("update_limit", broker(10))

	if err := service.SetMcpAccess(ctx, "add_limit", true); err != nil {
		t.Fatalf("switch on add_limit: %v", err)
	}
	stored := domain.LimitRate{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, Window: time.Minute, MaxOrders: 10}
	rate.accepts("add_limit", broker(10), "rate_limit broker added", stored)
	disabled("update_limit", broker(5), stored)
	if err := service.SetMcpAccess(ctx, "add_limit", false); err != nil {
		t.Fatalf("switch off add_limit: %v", err)
	}
	if err := service.SetMcpAccess(ctx, "update_limit", true); err != nil {
		t.Fatalf("switch on update_limit: %v", err)
	}
	stored.MaxOrders = 5
	rate.accepts("update_limit", broker(5), "rate_limit broker set", stored)

	if err := service.SetMcpAccess(ctx, "update_limit", false); err != nil {
		t.Fatalf("switch off update_limit: %v", err)
	}
	disabled("update_limit", broker(3), stored)
	disabled("add_limit", broker(3), stored)
}

// requireSpotFundsLimitMode fails the test unless the service stores want as
// the Spot Funds limit mode.
func requireSpotFundsLimitMode(t *testing.T, service *backend.Service, want domain.SpotFundsLimitMode) {
	t.Helper()
	state, err := service.SpotFundsLimitMode(systemCtx())
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != want {
		t.Fatalf("stored Spot Funds limit mode = %q, want %q", state.Mode, want)
	}
}

// putSpotFundsLimitMode sets the Spot Funds limit-mode meta-policy as the
// operator.
func putSpotFundsLimitMode(t *testing.T, service *backend.Service, mode domain.SpotFundsLimitMode) {
	t.Helper()
	if err := service.PutSpotFundsLimitMode(systemCtx(), domain.LimitSpotFundsLimitMode{
		Scope: domain.ScopeGlobal, Mode: mode,
	}); err != nil {
		t.Fatalf("put Spot Funds limit mode %s: %v", mode, err)
	}
}

// spotFundsLimitModeDriver drives update_limit with the spot_funds_limit_mode
// policy and reads the meta-policy back from the unified policy list, where
// it is one global row while a mode is stored.
func spotFundsLimitModeDriver(
	t *testing.T, service *backend.Service, src frameworkmcp.Source,
) limitWriteDriver[domain.LimitSpotFundsLimitMode] {
	return limitWriteDriver[domain.LimitSpotFundsLimitMode]{
		t: t, src: src,
		stored: func() []domain.LimitSpotFundsLimitMode {
			state, err := service.SpotFundsLimitMode(systemCtx())
			if err != nil {
				t.Fatalf("read funds limit mode: %v", err)
			}
			if !state.Stored {
				return nil
			}
			return []domain.LimitSpotFundsLimitMode{{Scope: domain.ScopeGlobal, Mode: state.Mode}}
		},
		body: func(limit domain.LimitSpotFundsLimitMode) map[string]any {
			return map[string]any{"spotFundsLimitMode": httpx.ToSpotFundsLimitModeLimitDTO(limit)}
		},
	}
}

// spotFundsLimitModeIn is the update_limit arguments for the meta-policy.
func spotFundsLimitModeIn(mode string) map[string]any {
	return map[string]any{"policy": "spot_funds_limit_mode", "scope": "global", "mode": mode}
}

// TestGetSpotFundsLimitModeOnControlPlane drives get_spot_funds_limit_mode
// through the control-plane Source of a service: it requires enforce on a
// fresh service and track_only after the operator changes the mode, with no
// accounts blocked for want of a currency in either response.
func TestGetSpotFundsLimitModeOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	for _, mode := range []domain.SpotFundsLimitMode{
		domain.SpotFundsLimitModeEnforce, domain.SpotFundsLimitModeTrackOnly,
		domain.SpotFundsLimitModeReviewWithHold, domain.SpotFundsLimitModeReviewWithoutHold,
	} {
		if mode != domain.SpotFundsLimitModeEnforce {
			putSpotFundsLimitMode(t, service, mode)
		}
		wire := callToolOK(t, src, "get_spot_funds_limit_mode", map[string]any{})
		// The fresh service stores no row; the operator's change stores one.
		assertResultBody(t, wire, map[string]any{
			"mode":                    string(mode),
			"stored":                  mode != domain.SpotFundsLimitModeEnforce,
			"accountsWithoutCurrency": []string{},
		})
	}
}

// TestUpdateLimitSpotFundsLimitModeOnControlPlane drives update_limit with the
// spot_funds_limit_mode policy through the control-plane Source of a service,
// as an agent. On a fresh service the agent may store enforce, the mode an
// absent row already means; after the operator stores track_only the agent's
// switch back to enforce succeeds, answers REST's body and is listed as the
// one global row, which get_limits carries beside the other barriers.
func TestUpdateLimitSpotFundsLimitModeOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	mode := spotFundsLimitModeDriver(t, service, src)
	const text = "spot_funds_limit_mode global set"
	enforce := domain.LimitSpotFundsLimitMode{Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeEnforce}

	mode.accepts("update_limit", spotFundsLimitModeIn("enforce"), text, enforce)
	putSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeTrackOnly)
	mode.accepts("update_limit", spotFundsLimitModeIn("enforce"), text, enforce)
	requireSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeEnforce)

	wire := callToolOK(t, src, "get_limits", map[string]any{})
	assertResultBody(t, wire, map[string]any{"limits": map[string]any{
		"rateLimits":                         []any{},
		"orderSizeLimits":                    []any{},
		"positionLimits":                     []any{},
		"spotFundsPnlBoundsLimits":           []any{},
		"spotFundsUnrealizedPnlBoundsLimits": []any{},
		"spotFundsTotalPnlBoundsLimits":      []any{},
		"spotFundsLimitModes":                []httpx.SpotFundsLimitModeLimitDTO{httpx.ToSpotFundsLimitModeLimitDTO(enforce)},
	}})
}

// TestUpdateLimitSpotFundsLimitModeRefusesLooseningOnControlPlane drives
// update_limit with the spot_funds_limit_mode policy through the control-plane
// Source of a service: the core refuses an agent's switch from enforce to
// track_only with loosening_refused, both with no row stored and with enforce
// stored, and the mode stays enforce. This is the tighten-only registry's
// proof that update_limit cannot loosen the mode.
func TestUpdateLimitSpotFundsLimitModeRefusesLooseningOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	mode := spotFundsLimitModeDriver(t, service, src)
	mode.refuses("update_limit", spotFundsLimitModeIn("track_only"), "loosening_refused")
	requireSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeEnforce)

	putSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeEnforce)
	enforce := domain.LimitSpotFundsLimitMode{Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeEnforce}
	mode.refuses("update_limit", spotFundsLimitModeIn("track_only"), "loosening_refused", enforce)
	requireSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeEnforce)
}

// TestUpdateLimitSpotFundsLimitModeRefusesInvalidOnControlPlane calls
// update_limit with the spot_funds_limit_mode policy through the control-plane
// Source of a service: an unsupported or absent mode and a scope other than
// global return the core's validation errors. Each field the meta-policy does
// not carry - an axis or another policy's value, sent empty as well - is
// refused by the tool, the mode field is refused for every other policy, and
// none of the calls stores a row.
func TestUpdateLimitSpotFundsLimitModeRefusesInvalidOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	mode := spotFundsLimitModeDriver(t, service, src)
	for _, review := range []string{"review_with_hold", "review_without_hold"} {
		res := callTool[map[string]any](t, src, "update_limit", spotFundsLimitModeIn(review))
		if text := textContent(res.Content); !res.IsError || !strings.HasPrefix(text, "validation: ") ||
			!strings.Contains(text, "review values of the fund-control setting are set through REST or the panel") {
			t.Fatalf("mode %s: %q, error=%t; want explicit review refusal", review, text, res.IsError)
		}
	}
	unsupported := callTool[map[string]any](t, src, "update_limit", spotFundsLimitModeIn("unsupported"))
	requireToolError(t, unsupported.IsError)
	if text := textContent(unsupported.Content); text != `validation: mode "unsupported" must be "enforce", "review_with_hold", "review_without_hold" or "track_only": invalid` {
		t.Fatalf("unsupported mode = %q, want core validation naming the mode", text)
	}
	absent := spotFundsLimitModeIn("")
	delete(absent, "mode")
	missing := callTool[map[string]any](t, src, "update_limit", absent)
	requireToolError(t, missing.IsError)
	if text := textContent(missing.Content); text != "validation: mode is required: invalid" {
		t.Fatalf("absent mode = %q, want core refusal of required mode", text)
	}
	for _, scope := range []any{"account", "broker", ""} {
		args := spotFundsLimitModeIn("enforce")
		args["scope"] = scope
		mode.refuses("update_limit", args, "validation")
	}
	for _, field := range []string{
		"account", "asset", "accountGroup", "windowMs", "maxOrders",
		"maxQuantity", "maxNotional", "currency", "lowerBound", "upperBound",
	} {
		values := []any{"1", ""}
		if field == "windowMs" || field == "maxOrders" {
			values = []any{1, 0}
		}
		for _, value := range values {
			args := spotFundsLimitModeIn("enforce")
			args[field] = value
			res := callTool[map[string]any](t, src, "update_limit", args)
			want := "validation: " + field + " is not a field of policy spot_funds_limit_mode"
			if text := textContent(res.Content); !res.IsError || !strings.HasPrefix(text, want) {
				t.Fatalf("update_limit with %s=%v = %q (tool error %t), want %q", field, value, text, res.IsError, want)
			}
		}
	}
	for _, in := range []map[string]any{
		{"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 10, "mode": "enforce"},
		{"policy": "order_size_limit", "scope": "broker", "maxQuantity": "5", "mode": ""},
	} {
		res := callTool[map[string]any](t, src, "update_limit", in)
		if text := textContent(res.Content); !res.IsError ||
			!strings.HasPrefix(text, "validation: mode is not a field of policy "+in["policy"].(string)) {
			t.Fatalf("update_limit %v = %q (tool error %t), want mode refused", in, text, res.IsError)
		}
	}
	if got := mode.stored(); len(got) != 0 {
		t.Fatalf("refused calls stored %+v, want no row", got)
	}
	requireNoLimits(t, service)
}

// TestUpdateLimitSpotFundsLimitModeDisabledChangesNothing calls update_limit
// with the spot_funds_limit_mode policy through the control-plane Source of a
// service as it ships and after the operator enables the tool, uses it and
// disables it again: each disabled switch from track_only to enforce returns
// the panel's notice as a tool error and leaves track_only stored.
func TestUpdateLimitSpotFundsLimitModeDisabledChangesNothing(t *testing.T) {
	t.Parallel()
	target, _ := newLocalNode(t)
	service := newOfficerService(t, target, nil, nil)
	putSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeTrackOnly)
	src := frameworkmcp.SourceFor(service)
	res := callTool[map[string]any](t, src, "update_limit", spotFundsLimitModeIn("enforce"))
	requireDisabledNotice(t, res.IsError, res.Content, "update_limit")
	requireSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeTrackOnly)

	if err := service.SetMcpAccess(systemCtx(), "update_limit", true); err != nil {
		t.Fatal(err)
	}
	wire := callToolOK(t, src, "update_limit", spotFundsLimitModeIn("enforce"))
	assertResultBody(t, wire, map[string]any{
		"spotFundsLimitMode": map[string]any{"scope": "global", "mode": "enforce"},
	})
	requireSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeEnforce)
	if err := service.SetMcpAccess(systemCtx(), "update_limit", false); err != nil {
		t.Fatal(err)
	}
	putSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeTrackOnly)
	res = callTool[map[string]any](t, src, "update_limit", spotFundsLimitModeIn("enforce"))
	requireDisabledNotice(t, res.IsError, res.Content, "update_limit")
	requireSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeTrackOnly)
}

// TestDeleteSpotFundsLimitModeOnControlPlane proves neither an operator nor an
// agent can delete the permanent mode or change it through a refused deletion.
func TestDeleteSpotFundsLimitModeOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	mode := spotFundsLimitModeDriver(t, service, src)
	putSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeTrackOnly)
	agent := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourceMCP, Principal: domain.PrincipalOperator,
	})
	target := node.LimitTarget{Policy: domain.PolicySpotFundsLimitMode, Scope: domain.ScopeGlobal}
	for _, ctx := range []context.Context{systemCtx(), agent} {
		if err := service.DeleteLimit(ctx, target); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("delete of permanent mode = %v, want ErrInvalid", err)
		}
		requireSpotFundsLimitMode(t, service, domain.SpotFundsLimitModeTrackOnly)
		want := []domain.LimitSpotFundsLimitMode{{Scope: domain.ScopeGlobal, Mode: domain.SpotFundsLimitModeTrackOnly}}
		if got := mode.stored(); !slices.Equal(got, want) {
			t.Fatalf("rows after refused delete = %+v, want %+v", got, want)
		}
	}
}

// oneAccountControlPlaneTools builds the Officer service over a local node in
// single-account mode for the account oneAccountFeatures fixes, under the
// account scope the composition loads when it starts, and the tool Source over
// it.
func oneAccountControlPlaneTools(t *testing.T) (*backend.Service, frameworkmcp.Source) {
	t.Helper()
	ctx := systemCtx()
	target, realm := newLocalNode(t)
	mode := accountmode.ModeSingle + ":" + string(oneAccountFeatures.Account)
	if err := realm.SetSetting(ctx, accountmode.SettingKeyMode, mode); err != nil {
		t.Fatalf("store account mode %s: %v", mode, err)
	}
	scope, err := accountmode.Load(ctx, target)
	if err != nil {
		t.Fatalf("load the account scope: %v", err)
	}
	service := newOfficerService(t, target, nil, nil, backend.WithAccountScope(scope))
	return service, enabledControlPlaneSource{frameworkmcp.SourceFor(service)}
}

// TestAddLimitWithOneAccount drives add_limit through the control-plane Source
// of a service in single-account mode: an account-scope barrier that names no
// account lands on the one account, and an account_group bound, whose group
// the input cannot carry there, is refused as the core refuses it over REST
// and stores nothing.
func TestAddLimitWithOneAccount(t *testing.T) {
	t.Parallel()
	service, src := oneAccountControlPlaneTools(t)
	if features := src.AccountFeatures(); features != oneAccountFeatures {
		t.Fatalf("account features = %+v, want %+v", features, oneAccountFeatures)
	}
	rateLimitDriver(t, service, src).accepts("add_limit", map[string]any{
		"policy": "rate_limit", "scope": "account", "windowMs": 1000, "maxOrders": 5,
	}, "rate_limit account added", domain.LimitRate{Mode: domain.RuleModeReject,
		Scope: domain.ScopeAccount, Account: oneAccountFeatures.Account,
		Window: time.Second, MaxOrders: 5,
	})

	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("create asset USD: %v", err)
	}
	restCtx := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourceAPI, Principal: domain.PrincipalOperator,
	})
	positionLimitDriver(t, service, src).accepts("add_limit", map[string]any{
		"policy": "position_limit", "scope": "account_asset", "asset": "USD", "maxPosition": "0",
	}, "position_limit account_asset added", domain.LimitPosition{Mode: domain.RuleModeReject,
		Scope: domain.ScopeAccountAsset, Account: oneAccountFeatures.Account, Asset: "USD", MaxPosition: "0",
	})
	rest := httpx.DescribeErr(service.PutSpotFundsPnlBoundsLimit(restCtx, domain.LimitSpotFundsPnlBounds{Mode: domain.RuleModeReject,
		Axis:  domain.SpotFundsPnlAxisRealized,
		Scope: domain.ScopeAccountGroup, Currency: "USD", LowerBound: "-1",
	}, domain.LimitWriteReplace, ""))
	if rest.Code != "validation" {
		t.Fatalf("service account_group bound = %+v, want a validation refusal", rest)
	}
	res := callTool[map[string]any](t, src, "add_limit", map[string]any{
		"policy": "spot_funds_pnl_bounds_kill_switch", "scope": "account_group",
		"currency": "USD", "lowerBound": "-1",
	})
	requireToolError(t, res.IsError)
	if got, want := textContent(res.Content), rest.Code+": "+rest.Message; got != want {
		t.Fatalf("add_limit of an account_group bound = %q, want REST's %q", got, want)
	}
	if got := storedLimits(t, service).SpotFundsPnlBoundsLimits; len(got) != 0 {
		t.Fatalf("stored bounds %+v, want none", got)
	}
}

// TestLimitWritesMissingAccountWithOneAccount drives both limit writes through
// the control-plane Source of a service in single-account mode, with a
// missingAccount that is no policy: an account-scope barrier that names no
// account ignores it, as REST ignores the query parameter of a request that
// names none, and lands on the one account; one that names the account is
// refused with validation and changes nothing.
func TestLimitWritesMissingAccountWithOneAccount(t *testing.T) {
	t.Parallel()
	service, src := oneAccountControlPlaneTools(t)
	rate := rateLimitDriver(t, service, src)
	stored := domain.LimitRate{Mode: domain.RuleModeReject,
		Scope: domain.ScopeAccount, Account: oneAccountFeatures.Account,
		Window: time.Second, MaxOrders: 5,
	}
	rate.accepts("add_limit", map[string]any{
		"policy": "rate_limit", "scope": "account", "windowMs": 1000, "maxOrders": 5,
		"missingAccount": "bogus",
	}, "rate_limit account added", stored)
	rate.refuses("update_limit", map[string]any{
		"policy": "rate_limit", "scope": "account", "account": string(oneAccountFeatures.Account),
		"windowMs": 1000, "maxOrders": 4, "missingAccount": "bogus",
	}, "validation", stored)

	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "ETH"}); err != nil {
		t.Fatal(err)
	}
	position := positionLimitDriver(t, service, src)
	positionStored := domain.LimitPosition{Mode: domain.RuleModeReject,
		Scope: domain.ScopeAccountAsset, Account: oneAccountFeatures.Account, Asset: "ETH", MaxPosition: "1",
	}
	position.accepts("add_limit", map[string]any{
		"policy": "position_limit", "scope": "account_asset", "asset": "ETH", "maxPosition": "1",
		"missingAccount": "bogus",
	}, "position_limit account_asset added", positionStored)
	position.refuses("update_limit", map[string]any{
		"policy": "position_limit", "scope": "account_asset", "account": string(oneAccountFeatures.Account),
		"asset": "ETH", "maxPosition": "0", "missingAccount": "bogus",
	}, "validation", positionStored)
}

// TestUpdateLimitRefusesUndeclaredField calls update_limit with an undeclared
// argument, in the default composition and in single-account mode, whose
// schema leaves that refusal to decoding: the call fails as a protocol error
// naming the argument, and nothing is stored.
func TestUpdateLimitRefusesUndeclaredField(t *testing.T) {
	t.Parallel()
	for name, tools := range map[string]func(*testing.T) (*backend.Service, frameworkmcp.Source){
		"default":     controlPlaneTools,
		"one account": oneAccountControlPlaneTools,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service, src := tools(t)
			ctx := testCtx(t)
			session, closeSession := officerSession(t, ctx, src)
			_, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
				Name: "update_limit",
				Arguments: map[string]any{
					"policy": "rate_limit", "scope": "broker", "windowMs": 60000,
					"maxOrders": 10, "undeclared": 0,
				},
			})
			closeSession()
			var rpcErr *jsonrpc.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams || !strings.Contains(err.Error(), `unknown field "undeclared"`) {
				t.Fatalf("call error = %v, want the undeclared field refused", err)
			}
			requireNoLimits(t, service)
		})
	}
}
