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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// setLimitDriver calls set_limit through src and checks the barriers of one
// kind that the service stores against what each call sends.
type setLimitDriver[Limit comparable] struct {
	t   *testing.T
	src frameworkmcp.Source
	// stored reads every barrier of the kind the service stores.
	stored func() []Limit
	// body is the body REST answers for a barrier it stored.
	body func(Limit) map[string]any
}

// accepts calls set_limit with in and requires it to answer text and the body
// REST answers for want, which the service stores as the only barrier of the
// kind.
func (d setLimitDriver[Limit]) accepts(in map[string]any, text string, want Limit) {
	d.t.Helper()
	res := callTool[map[string]any](d.t, d.src, "set_limit", in)
	if res.IsError {
		d.t.Fatalf("set_limit %v: %s", in, textContent(res.Content))
	}
	if got := textContent(res.Content); got != text {
		d.t.Fatalf("set_limit %v text = %q, want %q", in, got, text)
	}
	if got := d.stored(); !slices.Equal(got, []Limit{want}) {
		d.t.Fatalf("set_limit %v stored %+v, want %+v", in, got, want)
	}
	assertResultBody(d.t, res.Wire, d.body(want))
}

// refuses calls set_limit with in and requires a tool error opening with code
// that leaves the barriers of the kind as kept.
func (d setLimitDriver[Limit]) refuses(in map[string]any, code string, kept ...Limit) {
	d.t.Helper()
	res := callTool[map[string]any](d.t, d.src, "set_limit", in)
	if text := textContent(res.Content); !res.IsError || !strings.HasPrefix(text, code+": ") {
		d.t.Fatalf("set_limit %v = %q (tool error %t), want %s", in, text, res.IsError, code)
	}
	if got := d.stored(); !slices.Equal(got, kept) {
		d.t.Fatalf("set_limit %v left %+v stored, want %+v", in, got, kept)
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
) setLimitDriver[domain.LimitRate] {
	return setLimitDriver[domain.LimitRate]{
		t: t, src: src,
		stored: func() []domain.LimitRate { return storedLimits(t, service).RateLimits },
		body: func(limit domain.LimitRate) map[string]any {
			return map[string]any{"rateLimit": httpx.ToRateLimitDTO(limit)}
		},
	}
}

func orderSizeLimitDriver(
	t *testing.T, service *backend.Service, src frameworkmcp.Source,
) setLimitDriver[domain.LimitOrderSize] {
	return setLimitDriver[domain.LimitOrderSize]{
		t: t, src: src,
		stored: func() []domain.LimitOrderSize { return storedLimits(t, service).OrderSizeLimits },
		body: func(limit domain.LimitOrderSize) map[string]any {
			return map[string]any{"orderSizeLimit": httpx.ToOrderSizeLimitDTO(limit)}
		},
	}
}

func spotFundsPnlBoundsDriver(
	t *testing.T, service *backend.Service, src frameworkmcp.Source,
) setLimitDriver[domain.LimitSpotFundsPnlBounds] {
	return setLimitDriver[domain.LimitSpotFundsPnlBounds]{
		t: t, src: src,
		stored: func() []domain.LimitSpotFundsPnlBounds {
			return storedLimits(t, service).SpotFundsPnlBoundsLimits
		},
		body: func(limit domain.LimitSpotFundsPnlBounds) map[string]any {
			return map[string]any{
				"spotFundsPnlBoundsLimit": httpx.ToSpotFundsPnlBoundsLimitDTO(limit),
			}
		},
	}
}

// requireNoLimits fails the test unless the service stores no barrier.
func requireNoLimits(t *testing.T, service *backend.Service) {
	t.Helper()
	limits := storedLimits(t, service)
	if len(limits.RateLimits)+len(limits.OrderSizeLimits)+len(limits.SpotFundsPnlBoundsLimits) != 0 {
		t.Fatalf("stored barriers %+v, want none", limits)
	}
}

// TestSetLimitRateOnControlPlane drives set_limit with the rate_limit policy
// through the control-plane Source into a local service, as an agent: a new
// barrier and a stricter one are stored exactly as sent and answered with
// REST's body, which get_limits reads back in the same shape. More orders and
// another window on the broker scope are refused with loosening_refused, a
// field of another policy with validation, sent empty as well, and each leaves
// the barrier as it was.
func TestSetLimitRateOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	rate := rateLimitDriver(t, service, src)

	rate.accepts(map[string]any{
		"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 10,
	}, "rate_limit broker set", domain.LimitRate{
		Scope: domain.ScopeBroker, Window: time.Minute, MaxOrders: 10,
	})
	stricter := domain.LimitRate{Scope: domain.ScopeBroker, Window: time.Minute, MaxOrders: 5}
	rate.accepts(map[string]any{
		"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 5,
	}, "rate_limit broker set", stricter)

	for _, in := range []map[string]any{
		// More orders.
		{"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 6},
		// A broker barrier counts in a fixed window, so a longer one loosens
		// too.
		{"policy": "rate_limit", "scope": "broker", "windowMs": 120000, "maxOrders": 5},
	} {
		rate.refuses(in, "loosening_refused", stricter)
	}
	// An empty field decodes as an omitted one, and is refused all the same.
	for _, quantity := range []string{"1", ""} {
		rate.refuses(map[string]any{
			"policy": "rate_limit", "scope": "broker", "windowMs": 60000, "maxOrders": 4,
			"maxQuantity": quantity,
		}, "validation", stricter)
	}

	wire := callToolOK(t, src, "get_limits", map[string]any{})
	assertResultBody(t, wire, map[string]any{"limits": map[string]any{
		"rateLimits":               []httpx.RateLimitDTO{httpx.ToRateLimitDTO(stricter)},
		"orderSizeLimits":          []any{},
		"spotFundsPnlBoundsLimits": []any{},
	}})
}

// TestSetLimitOrderSizeOnControlPlane drives set_limit with the
// order_size_limit policy through the control-plane Source into a local
// service, as an agent: a new barrier and a stricter one are stored exactly as
// sent, digits float64 cannot hold included, and answered with REST's body. A
// cap raised by the last of those digits and a dropped cap are refused with
// loosening_refused, a field of another policy with validation, sent as zero
// as well, and each leaves the barrier as it was.
func TestSetLimitOrderSizeOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	size := orderSizeLimitDriver(t, service, src)

	size.accepts(map[string]any{
		"policy": "order_size_limit", "scope": "broker",
		"maxQuantity": "10.0000000000000000001", "maxNotional": "1000",
	}, "order_size_limit broker set", domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxQuantity: "10.0000000000000000001", MaxNotional: "1000",
	})
	stricter := domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxQuantity: "5.0000000000000000001", MaxNotional: "1000",
	}
	size.accepts(map[string]any{
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
		size.refuses(in, "loosening_refused", stricter)
	}
	// A zero field decodes as an omitted one, and is refused all the same.
	for _, window := range []int{1000, 0} {
		size.refuses(map[string]any{
			"policy": "order_size_limit", "scope": "broker",
			"maxQuantity": "4", "maxNotional": "1000", "windowMs": window,
		}, "validation", stricter)
	}
}

// TestSetLimitSpotFundsPnlBoundsOnControlPlane drives set_limit with the
// spot_funds_pnl_bounds_kill_switch policy through the control-plane Source
// into a local service, as an agent: a new bound and a stricter one are stored
// exactly as sent and answered with REST's body. A lower bound lowered by
// 1e-19, a dropped bound and bounds in another currency are refused with
// loosening_refused, a field of another policy with validation, sent empty or
// null as well, and each leaves the bound as it was.
func TestSetLimitSpotFundsPnlBoundsOnControlPlane(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	for _, code := range []string{"USD", "EUR"} {
		if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: code}); err != nil {
			t.Fatalf("create asset %s: %v", code, err)
		}
	}
	bounds := spotFundsPnlBoundsDriver(t, service, src)
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

	bounds.accepts(global("USD", "-100", "100"), text, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-100", UpperBound: "100",
	})
	stricter := domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeGlobal, Currency: "USD", LowerBound: "-50", UpperBound: "100",
	}
	bounds.accepts(global("USD", "-50", "100"), text, stricter)

	for _, in := range []map[string]any{
		// The lower bound lowered by 1e-19.
		global("USD", "-50.0000000000000000001", "100"),
		// The upper bound dropped.
		global("USD", "-50", ""),
		// The bounds moved to another currency.
		global("EUR", "-50", "100"),
	} {
		bounds.refuses(in, "loosening_refused", stricter)
	}
	// An empty or null field decodes as an omitted one, and is refused all the
	// same.
	for _, asset := range []any{"USD", "", nil} {
		foreign := global("USD", "-40", "100")
		foreign["asset"] = asset
		bounds.refuses(foreign, "validation", stricter)
	}
}

// TestSetLimitRefusesUnknownPolicy drives set_limit naming a policy it does
// not set: the call is refused with validation naming the policy, and stores
// nothing.
func TestSetLimitRefusesUnknownPolicy(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	res := callTool[map[string]any](t, src, "set_limit", map[string]any{
		"policy": "kill_switch", "scope": "broker", "windowMs": 60000, "maxOrders": 10,
	})
	requireToolError(t, res.IsError)
	if text := textContent(res.Content); !strings.HasPrefix(text, `validation: policy "kill_switch"`) {
		t.Fatalf("set_limit of an unknown policy = %q, want validation naming it", text)
	}
	requireNoLimits(t, service)
}

// TestSetLimitRefusesUnrepresentableWindow drives set_limit with a rate window
// whose nanoseconds do not fit time.Duration: 2^58 + 60000 milliseconds, which
// a wrapping conversion would turn into a window of one minute. The call is
// refused with validation and stores nothing.
func TestSetLimitRefusesUnrepresentableWindow(t *testing.T) {
	t.Parallel()
	service, src := controlPlaneTools(t)
	rateLimitDriver(t, service, src).refuses(map[string]any{
		"policy": "rate_limit", "scope": "broker",
		"windowMs": int64(288230376151771744), "maxOrders": 10,
	}, "validation")
}

// TestSetLimitRefusesBoundsTheEngineCannotParse drives set_limit with Spot
// Funds P&L bounds the engine cannot hold as written: a lower bound whose
// exponent a comparison would rescale by for minutes is refused with
// validation within a second, and one in exponent notation, which the engine's
// exact parser rejects, with validation too. Neither stores a bound or
// rebuilds the engine, which would reset every rate window; the first
// order-size barrier then shows that the build count sees a rebuild. It does
// not run in parallel, so the deadline measures the refusal and not the
// scheduler.
func TestSetLimitRefusesBoundsTheEngineCannotParse(t *testing.T) {
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
	bounds := spotFundsPnlBoundsDriver(t, service, src)
	global := func(lower string) map[string]any {
		return map[string]any{
			"policy": "spot_funds_pnl_bounds_kill_switch", "scope": "global",
			"currency": "USD", "lowerBound": lower, "upperBound": "0",
		}
	}

	start := time.Now()
	bounds.refuses(global("-1e-2000000000"), "validation")
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("the refusal took %s, want it within a second", elapsed)
	}
	bounds.refuses(global("-1e2"), "validation")
	if got := builds.Load(); got != built {
		t.Fatalf("engine builds went from %d to %d, want the refusals to rebuild none", built, got)
	}

	// The count sees a rebuild: the first order-size barrier is installed by
	// one.
	orderSizeLimitDriver(t, service, src).accepts(map[string]any{
		"policy": "order_size_limit", "scope": "broker", "maxQuantity": "1",
	}, "order_size_limit broker set", domain.LimitOrderSize{
		Scope: domain.ScopeBroker, MaxQuantity: "1",
	})
	if builds.Load() == built {
		t.Fatal("the first order-size barrier rebuilt the engine past the count")
	}
}

// TestSetLimitDisabledChangesNothing drives set_limit through the
// control-plane Source of a service as it ships, with set_limit disabled, and
// after the operator switches it on, stores a barrier through it and switches
// it off again: while it is disabled the agent receives the panel's notice,
// not an error, and the call changes no barrier.
func TestSetLimitDisabledChangesNothing(t *testing.T) {
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
	// disabled calls set_limit with in and requires the panel's notice that
	// leaves the barriers as kept.
	disabled := func(in map[string]any, kept ...domain.LimitRate) {
		t.Helper()
		res := callTool[map[string]any](t, src, "set_limit", in)
		requireNotToolError(t, res.IsError)
		if text := textContent(res.Content); !strings.Contains(
			text, `Command "set_limit" is disabled in the Pit Officer panel`,
		) {
			t.Fatalf("disabled set_limit = %q, want the panel's notice", text)
		}
		if got := rate.stored(); !slices.Equal(got, kept) {
			t.Fatalf("disabled set_limit left %+v stored, want %+v", got, kept)
		}
	}

	disabled(broker(10))

	if err := service.SetMcpAccess(ctx, "set_limit", true); err != nil {
		t.Fatalf("switch on set_limit: %v", err)
	}
	stored := domain.LimitRate{Scope: domain.ScopeBroker, Window: time.Minute, MaxOrders: 10}
	rate.accepts(broker(10), "rate_limit broker set", stored)

	if err := service.SetMcpAccess(ctx, "set_limit", false); err != nil {
		t.Fatalf("switch off set_limit: %v", err)
	}
	disabled(broker(5), stored)
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

// TestSetLimitWithOneAccount drives set_limit through the control-plane Source
// of a service in single-account mode: an account-scope barrier that names no
// account lands on the one account, and an account_group bound, whose group
// the input cannot carry there, is refused as the core refuses it over REST
// and stores nothing.
func TestSetLimitWithOneAccount(t *testing.T) {
	t.Parallel()
	service, src := oneAccountControlPlaneTools(t)
	if features := src.AccountFeatures(); features != oneAccountFeatures {
		t.Fatalf("account features = %+v, want %+v", features, oneAccountFeatures)
	}
	rateLimitDriver(t, service, src).accepts(map[string]any{
		"policy": "rate_limit", "scope": "account", "windowMs": 1000, "maxOrders": 5,
	}, "rate_limit account set", domain.LimitRate{
		Scope: domain.ScopeAccount, Account: oneAccountFeatures.Account,
		Window: time.Second, MaxOrders: 5,
	})

	if _, err := service.CreateAsset(systemCtx(), domain.Asset{Code: "USD"}); err != nil {
		t.Fatalf("create asset USD: %v", err)
	}
	restCtx := auth.ContextWithCaller(context.Background(), domain.Caller{
		Source: domain.SourceAPI, Principal: domain.PrincipalOperator,
	})
	rest := httpx.DescribeErr(service.PutSpotFundsPnlBoundsLimit(restCtx, domain.LimitSpotFundsPnlBounds{
		Scope: domain.ScopeAccountGroup, Currency: "USD", LowerBound: "-1",
	}, ""))
	if rest.Code != "validation" {
		t.Fatalf("service account_group bound = %+v, want a validation refusal", rest)
	}
	res := callTool[map[string]any](t, src, "set_limit", map[string]any{
		"policy": "spot_funds_pnl_bounds_kill_switch", "scope": "account_group",
		"currency": "USD", "lowerBound": "-1",
	})
	requireToolError(t, res.IsError)
	if got, want := textContent(res.Content), rest.Code+": "+rest.Message; got != want {
		t.Fatalf("set_limit of an account_group bound = %q, want REST's %q", got, want)
	}
	if got := storedLimits(t, service).SpotFundsPnlBoundsLimits; len(got) != 0 {
		t.Fatalf("stored bounds %+v, want none", got)
	}
}

// TestSetLimitMissingAccountWithOneAccount drives set_limit through the
// control-plane Source of a service in single-account mode, with a
// missingAccount that is no policy: an account-scope barrier that names no
// account ignores it, as REST ignores the query parameter of a request that
// names none, and lands on the one account; one that names the account is
// refused with validation and changes nothing.
func TestSetLimitMissingAccountWithOneAccount(t *testing.T) {
	t.Parallel()
	service, src := oneAccountControlPlaneTools(t)
	rate := rateLimitDriver(t, service, src)
	stored := domain.LimitRate{
		Scope: domain.ScopeAccount, Account: oneAccountFeatures.Account,
		Window: time.Second, MaxOrders: 5,
	}
	rate.accepts(map[string]any{
		"policy": "rate_limit", "scope": "account", "windowMs": 1000, "maxOrders": 5,
		"missingAccount": "bogus",
	}, "rate_limit account set", stored)
	rate.refuses(map[string]any{
		"policy": "rate_limit", "scope": "account", "account": string(oneAccountFeatures.Account),
		"windowMs": 1000, "maxOrders": 4, "missingAccount": "bogus",
	}, "validation", stored)
}

// TestSetLimitRefusesUndeclaredField calls set_limit with an argument it does
// not declare, in the default composition and in single-account mode, whose
// schema leaves that refusal to decoding: the call fails as a protocol error
// naming the argument, and nothing is stored.
func TestSetLimitRefusesUndeclaredField(t *testing.T) {
	t.Parallel()
	for name, tools := range map[string]func(*testing.T) (*backend.Service, frameworkmcp.Source){
		"default":     controlPlaneTools,
		"one account": oneAccountControlPlaneTools,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service, src := tools(t)
			ctx, cancel := context.WithTimeout(systemCtx(), 5*time.Second)
			defer cancel()
			session, closeSession := officerSession(t, ctx, src)
			_, err := session.CallTool(ctx, &sdkmcp.CallToolParams{
				Name: "set_limit",
				Arguments: map[string]any{
					"policy": "rate_limit", "scope": "broker", "windowMs": 60000,
					"maxOrders": 10, "undeclared": 0,
				},
			})
			closeSession()
			if err == nil || !strings.Contains(err.Error(), `unknown field "undeclared"`) {
				t.Fatalf("call error = %v, want the undeclared field refused", err)
			}
			requireNoLimits(t, service)
		})
	}
}
