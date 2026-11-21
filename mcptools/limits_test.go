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

	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

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
	// MCP writes only reject order-size barriers; mode is a foreign field.
	delete(bodies[domain.PolicyOrderSizeLimit], "mode")
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
