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
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	fwstore "go.openpit.dev/officer/framework/store"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/store/sqlite"
)

type countingModeSource struct {
	frameworkmcp.Source
	realm fwstore.RealmStore
	write domain.LimitWrite
}

type spotFundsModeWriteProbe struct {
	frameworkmcp.Source
	calls int
}

func (s *spotFundsModeWriteProbe) PutSpotFundsLimitMode(context.Context, domain.LimitSpotFundsLimitMode) error {
	s.calls++
	return errors.New("unexpected mode write")
}

func TestUpdateSpotFundsLimitModeRefusesReviewBeforeSource(t *testing.T) {
	for _, mode := range []domain.SpotFundsLimitMode{
		domain.SpotFundsLimitModeReviewWithHold, domain.SpotFundsLimitModeReviewWithoutHold,
	} {
		t.Run(string(mode), func(t *testing.T) {
			src := &spotFundsModeWriteProbe{}
			input := limitWriteInput{Policy: domain.PolicySpotFundsLimitMode, Scope: "global", Mode: string(mode)}
			_, err := putSpotFundsLimitMode(t.Context(), src, input, domain.LimitWriteChange)
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/mode" ||
				domain.ValidationConstraint(err) != "unsupported_value" {
				t.Fatalf("error = %v, want unsupported_value at /mode wrapping ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), "REST or the panel") {
				t.Fatalf("error = %v, want setting route", err)
			}
			_, _, toolErr := updateLimitHandler(src)(t.Context(), nil, input)
			if toolErr == nil || !strings.HasPrefix(toolErr.Error(), "validation:") {
				t.Fatalf("tool error = %v, want validation shape", toolErr)
			}
			if src.calls != 0 {
				t.Fatalf("source calls = %d, want no storage or engine call", src.calls)
			}
		})
	}
}

func (s *countingModeSource) AccountFeatures() backend.AccountFeatures {
	return backend.AccountFeatures{}
}

func (s *countingModeSource) PutRateLimit(ctx context.Context, limit domain.LimitRate, write domain.LimitWrite, _ domain.MissingAccountPolicy) error {
	s.write = write
	return s.realm.PutRateLimit(ctx, limit)
}

func (s *countingModeSource) PutPositionLimit(ctx context.Context, limit domain.LimitPosition, write domain.LimitWrite, _ domain.MissingAccountPolicy) error {
	s.write = write
	return s.realm.PutPositionLimit(ctx, limit)
}

func (s *countingModeSource) PutSpotFundsPnlBoundsLimit(ctx context.Context, limit domain.LimitSpotFundsPnlBounds, write domain.LimitWrite, _ domain.MissingAccountPolicy) error {
	s.write = write
	return s.realm.PutSpotFundsPnlBoundsLimit(ctx, limit)
}

func (s *countingModeSource) ListLimits(ctx context.Context, account domain.AccountID) (node.AccountLimits, error) {
	rate, err := s.realm.ListRateLimits(ctx, account)
	if err != nil {
		return node.AccountLimits{}, err
	}
	position, err := s.realm.ListPositionLimits(ctx, account)
	if err != nil {
		return node.AccountLimits{}, err
	}
	pnl, err := s.realm.ListSpotFundsPnlBoundsLimits(ctx, account)
	if err != nil {
		return node.AccountLimits{}, err
	}
	return node.AccountLimits{RateLimits: rate, PositionLimits: position, SpotFundsPnlBoundsLimits: pnl}, nil
}

func TestCountingLimitMCPUpdatesOnlyReject(t *testing.T) {
	for _, policy := range []string{domain.PolicyRateLimit, domain.PolicyPositionLimit,
		domain.PolicySpotFundsPnlBoundsKillSwitch, domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch,
		domain.PolicySpotFundsTotalPnlBoundsKillSwitch} {
		t.Run(policy, func(t *testing.T) {
			ctx := t.Context()
			db, err := sqlite.New(t.TempDir()+"/limits.db", domain.DefaultRealm)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := db.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			realm, err := db.ForRealm(ctx, domain.DefaultRealm)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := realm.CreateAsset(ctx, domain.Asset{Code: "AAPL"}); err != nil {
				t.Fatal(err)
			}
			if _, err := realm.CreateAccount(ctx, domain.Account{Code: "acc-1"}); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []domain.RuleMode{domain.RuleModeReject, domain.RuleModeReviewWithoutHold} {
				switch policy {
				case domain.PolicyRateLimit:
					err = realm.PutRateLimit(ctx, domain.LimitRate{Mode: mode, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", Window: time.Second, MaxOrders: 10})
				case domain.PolicyPositionLimit:
					err = realm.PutPositionLimit(ctx, domain.LimitPosition{Mode: mode, Scope: domain.ScopeAccountAsset, Account: "acc-1", Asset: "AAPL", MaxPosition: "10"})
				default:
					axis, ok := domain.SpotFundsPnlAxisOf(policy)
					if !ok {
						t.Fatal("missing P&L axis")
					}
					err = realm.PutSpotFundsPnlBoundsLimit(ctx, domain.LimitSpotFundsPnlBounds{Mode: mode, Axis: axis, Scope: domain.ScopeGlobal, Currency: "AAPL", LowerBound: "-10"})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			src := &countingModeSource{realm: realm}
			input := limitWriteInput{Policy: policy, Scope: "account_asset", Account: "acc-1", Asset: "AAPL", WindowMs: 1000, MaxOrders: 5, MaxPosition: "5"}
			if _, ok := domain.SpotFundsPnlAxisOf(policy); ok {
				input = limitWriteInput{Policy: policy, Scope: "global", Currency: "AAPL", LowerBound: "-5"}
			}
			_, out, err := updateLimitHandler(src)(ctx, nil, input)
			if err != nil {
				t.Fatal(err)
			}
			if src.write != domain.LimitWriteChange {
				t.Fatalf("write = %s", src.write)
			}
			limits, err := src.ListLimits(ctx, "")
			if err != nil {
				t.Fatal(err)
			}
			switch policy {
			case domain.PolicyRateLimit:
				if out.RateLimit == nil || out.RateLimit.Mode != "reject" || out.RateLimit.MaxOrders != 5 || len(limits.RateLimits) != 2 {
					t.Fatalf("MCP response=%+v stored=%+v", out, limits)
				}
				for _, limit := range limits.RateLimits {
					want := uint64(10)
					if limit.Mode == domain.RuleModeReject {
						want = 5
					}
					if limit.MaxOrders != want {
						t.Fatalf("stored rate = %+v, want %d", limit, want)
					}
				}
			case domain.PolicyPositionLimit:
				if out.PositionLimit == nil || out.PositionLimit.Mode != "reject" || out.PositionLimit.MaxPosition != "5" || len(limits.PositionLimits) != 2 {
					t.Fatalf("MCP response=%+v stored=%+v", out, limits)
				}
				for _, limit := range limits.PositionLimits {
					want := "10"
					if limit.Mode == domain.RuleModeReject {
						want = "5"
					}
					if limit.MaxPosition != want {
						t.Fatalf("stored position = %+v, want %s", limit, want)
					}
				}
			default:
				bound := out.SpotFundsPnlBoundsLimit
				switch policy {
				case domain.PolicySpotFundsUnrealizedPnlBoundsKillSwitch:
					bound = out.SpotFundsUnrealizedPnlBoundsLimit
				case domain.PolicySpotFundsTotalPnlBoundsKillSwitch:
					bound = out.SpotFundsTotalPnlBoundsLimit
				}
				if bound == nil || bound.Mode != "reject" || bound.LowerBound != "-5" || len(limits.SpotFundsPnlBoundsLimits) != 2 {
					t.Fatalf("MCP response=%+v stored=%+v", out, limits)
				}
				for _, limit := range limits.SpotFundsPnlBoundsLimits {
					want := "-10"
					if limit.Mode == domain.RuleModeReject {
						want = "-5"
						key, err := limit.Key()
						if err != nil {
							t.Fatal(err)
						}
						if err := realm.DeleteLimit(ctx, key); err != nil {
							t.Fatal(err)
						}
					}
					if limit.LowerBound != want {
						t.Fatalf("stored P&L bound = %+v, want %s", limit, want)
					}
				}
				if _, _, err := addLimitHandler(src)(ctx, nil, input); err != nil || src.write != domain.LimitWriteAdd {
					t.Fatalf("add reject barrier: write=%s error=%v", src.write, err)
				}
				limits, err = src.ListLimits(ctx, "")
				if err != nil || len(limits.SpotFundsPnlBoundsLimits) != 2 {
					t.Fatalf("after add: %+v, %v", limits, err)
				}
				for _, limit := range limits.SpotFundsPnlBoundsLimits {
					want := "-10"
					if limit.Mode == domain.RuleModeReject {
						want = "-5"
					}
					if limit.LowerBound != want {
						t.Fatalf("added P&L bound = %+v, want %s", limit, want)
					}
				}
			}
		})
	}
}

type orderSizeModeSource struct {
	frameworkmcp.Source
	limits  []domain.LimitOrderSize
	written domain.LimitOrderSize
	write   domain.LimitWrite
}

func (s *orderSizeModeSource) PutOrderSizeLimit(
	_ context.Context, limit domain.LimitOrderSize, write domain.LimitWrite, _ domain.MissingAccountPolicy,
) error {
	s.written, s.write = limit, write
	return nil
}

func (s *orderSizeModeSource) ListLimits(context.Context, domain.AccountID) (node.AccountLimits, error) {
	return node.AccountLimits{OrderSizeLimits: s.limits}, nil
}

func TestOrderSizeModeWriteAnswer(t *testing.T) {
	for _, write := range []domain.LimitWrite{domain.LimitWriteAdd, domain.LimitWriteChange} {
		t.Run(string(write), func(t *testing.T) {
			src := &orderSizeModeSource{limits: []domain.LimitOrderSize{
				{Mode: domain.RuleModeReviewWithoutHold, Scope: domain.ScopeBroker, MaxQuantity: "90"},
				{Mode: domain.RuleModeReject, Scope: domain.ScopeBroker, MaxQuantity: "11"},
			}}
			handler := addLimitHandler(src)
			if write == domain.LimitWriteChange {
				handler = updateLimitHandler(src)
			}
			_, out, err := handler(context.Background(), nil, limitWriteInput{Policy: domain.PolicyOrderSizeLimit, Scope: "broker", MaxQuantity: "10"})
			if err != nil {
				t.Fatal(err)
			}
			if src.written.Mode != domain.RuleModeReject || src.write != write {
				t.Fatalf("MCP write = %+v / %s", src.written, src.write)
			}
			if out.OrderSizeLimit == nil || out.OrderSizeLimit.Mode != "reject" || out.OrderSizeLimit.MaxQuantity != "11" {
				t.Fatalf("MCP write answered with neighbour mode: %+v", out.OrderSizeLimit)
			}
		})
	}
}

func schemaFieldName(field reflect.StructField) (string, bool) {
	if !field.IsExported() {
		return "", false
	}
	tag, ok := field.Tag.Lookup("json")
	if !ok {
		return field.Name, true
	}
	name, _, options := strings.Cut(tag, ",")
	if name == "-" && !options {
		return "", false
	}
	if name == "" {
		return field.Name, true
	}
	return name, true
}

// jsonFields maps the JSON name of each field of the struct type t that
// encoding/json and the schema inference carry, as schemaFieldName names it,
// to the field's type.
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := range t.NumField() {
		field := t.Field(i)
		name, ok := schemaFieldName(field)
		if !ok {
			continue
		}
		fields[name] = field.Type
	}
	return fields
}

// TestLimitWriteInputCarriesLimitBodies reads the fields of each limit put's
// body from its REST DTO: limitWriteInput declares every one of them under its
// JSON name and type, and for each policy refuseForeignFields accepts every
// field of that policy's body and refuses every field of another body that
// the policy's own lacks with a validation error naming it. Each field is sent
// with its zero value, which decodes as if it were omitted.
func TestLimitWriteInputCarriesLimitBodies(t *testing.T) {
	bodies := map[string]map[string]reflect.Type{
		domain.PolicyRateLimit: jsonFields(reflect.TypeFor[httpx.RateLimitDTO]()),
		domain.PolicyOrderSizeLimit: jsonFields(
			reflect.TypeFor[httpx.OrderSizeLimitDTO](),
		),
		domain.PolicyPositionLimit: jsonFields(reflect.TypeFor[httpx.PositionLimitDTO]()),
		domain.PolicySpotFundsPnlBoundsKillSwitch: jsonFields(
			reflect.TypeFor[httpx.SpotFundsPnlBoundsLimitDTO](),
		),
	}
	declared := jsonFields(reflect.TypeFor[limitWriteInput]())
	// MCP writes only reject barriers; rule mode is a foreign field.
	for policy, body := range bodies {
		if domain.LimitPolicyHasMode(policy) {
			delete(body, "mode")
		}
	}
	every := map[string]reflect.Type{}
	every["mode"] = reflect.TypeFor[string]()
	for policy, body := range bodies {
		for name, typ := range body {
			if got, ok := declared[name]; !ok || got != typ {
				t.Errorf("limitWriteInput declares %s as %v, want %v as the %s body does",
					name, got, typ, policy)
			}
			every[name] = typ
		}
	}

	for policy, body := range bodies {
		for name, typ := range every {
			args, err := json.Marshal(map[string]any{
				"policy": policy, name: reflect.Zero(typ).Interface(),
			})
			if err != nil {
				t.Fatalf("marshal the arguments of %s with %s: %v", policy, name, err)
			}
			var in limitWriteInput
			if err := json.Unmarshal(args, &in); err != nil {
				t.Fatalf("decode %s: %v", args, err)
			}
			err = in.refuseForeignFields()
			if _, own := body[name]; own {
				if err != nil {
					t.Errorf("%s refuses %s of its own body: %v", args, name, err)
				}
				continue
			}
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/"+name {
				t.Errorf("%s = %v, want a validation error on /%s", args, err, name)
			}
		}
	}
}

// TestLimitWriteInputRefusesForeignKeyInAnyCase sends another policy's field
// under a key that differs from its name only in case, which decoding matches
// to the field as it matches any key: it is refused as the field is.
func TestLimitWriteInputRefusesForeignKeyInAnyCase(t *testing.T) {
	const args = `{"policy": "order_size_limit", "WindowMS": 0}`
	var in limitWriteInput
	if err := json.Unmarshal([]byte(args), &in); err != nil {
		t.Fatalf("decode %s: %v", args, err)
	}
	if err := in.refuseForeignFields(); domain.ValidationPointer(err) != "/windowMs" {
		t.Fatalf("%s = %v, want a validation error on /windowMs", args, err)
	}
}
