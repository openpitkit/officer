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
// Please see https://openpit.dev and the OWNERS file for details.

package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

func TestToolRegistryReplaceAndUnregister(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(ToolDescriptor{
		Name:             "alpha",
		Title:            "Alpha",
		AgentDescription: "first",
		DefaultEnabled:   true,
	})
	reg.Register(ToolDescriptor{
		Name:             "beta",
		Title:            "Beta",
		AgentDescription: "second",
		Mutating:         true,
	})
	reg.Register(ToolDescriptor{
		Name:             "alpha",
		Title:            "Alpha replacement",
		AgentDescription: "replacement",
		Mutating:         true,
		Protective:       true,
		Implemented:      true,
	})

	got := reg.Descriptors()
	if len(got) != 2 {
		t.Fatalf("descriptor count: want 2 got %d", len(got))
	}
	if got[0].Name != "alpha" || got[0].Title != "Alpha replacement" {
		t.Fatalf("replace should preserve position and update descriptor: %+v", got)
	}
	if got[1].Name != "beta" {
		t.Fatalf("replace should not reorder later descriptors: %+v", got)
	}

	cat := reg.Catalog().All()
	if len(cat) != 2 || cat[0].Name != "alpha" || cat[0].Title != "Alpha replacement" {
		t.Fatalf("catalog should reflect replacement: %+v", cat)
	}

	reg.Unregister("alpha")
	got = reg.Descriptors()
	if len(got) != 1 || got[0].Name != "beta" {
		t.Fatalf("unregister should remove descriptor: %+v", got)
	}
	cat = reg.Catalog().All()
	if len(cat) != 1 || cat[0].Name != "beta" {
		t.Fatalf("catalog should reflect unregister: %+v", cat)
	}
}

func TestGuardAuthorizerAndPanelGates(t *testing.T) {
	ctx := context.Background()
	allowSrc := &guardSource{enabled: true}
	read := ToolDescriptor{Name: "read", DefaultEnabled: true}
	handler := Guard(
		read,
		RegisterDeps{Source: allowSrc, Authorizer: allowAuthorizer{}},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			return "ran", "ok", nil
		},
	)
	res, err := handler(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("allow handler returned error: %v", err)
	}
	if res.IsError || res.StructuredContent != "ok" {
		t.Fatalf("allow handler result mismatch: %+v", res)
	}

	deny := Guard(
		read,
		RegisterDeps{Source: allowSrc, Authorizer: denyAuthorizer{}},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			t.Fatal("denied tool body must not run")
			return "", "", nil
		},
	)
	res, err = deny(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("deny handler returned transport error: %v", err)
	}
	if !res.IsError || !contentContains(res.Content, "permission denied") {
		t.Fatalf("deny should return permission error result: %+v", res)
	}

	disabled := Guard(
		read,
		RegisterDeps{Source: &guardSource{enabled: false}, Authorizer: allowAuthorizer{}},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			t.Fatal("disabled tool body must not run")
			return "", "", nil
		},
	)
	res, err = disabled(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("disabled handler returned transport error: %v", err)
	}
	if res.IsError || !contentContains(res.Content, "disabled in the Pit Officer panel") {
		t.Fatalf("disabled should return non-error panel notice: %+v", res)
	}
}

func TestGuardGatePolicy(t *testing.T) {
	ctx := context.Background()
	readRan := false
	read := Guard(
		ToolDescriptor{Name: "read"},
		RegisterDeps{Source: &guardSource{err: errors.New("store down")}},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			readRan = true
			return "read", "ok", nil
		},
	)
	res, err := read(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("read handler returned error: %v", err)
	}
	if !readRan || res.IsError {
		t.Fatalf("read gate should fail open and run: ran=%v res=%+v", readRan, res)
	}

	mutatingRan := false
	mutating := Guard(
		ToolDescriptor{Name: "write", Mutating: true},
		RegisterDeps{Source: &guardSource{err: errors.New("store down")}},
		func(context.Context, *sdkmcp.ServerSession, struct{}) (string, string, error) {
			mutatingRan = true
			return "write", "ok", nil
		},
	)
	res, err = mutating(ctx, nil, &sdkmcp.CallToolParamsFor[struct{}]{})
	if err != nil {
		t.Fatalf("mutating handler returned transport error: %v", err)
	}
	if mutatingRan {
		t.Fatal("mutating gate must fail closed and skip body")
	}
	if !res.IsError || !contentContains(res.Content, "command access check failed; command not executed") {
		t.Fatalf("mutating gate should return fail-closed tool error: %+v", res)
	}
}

func TestCatalogCopy(t *testing.T) {
	reg := NewToolRegistry()
	reg.Register(ToolDescriptor{Name: "alpha", Title: "Alpha"})
	cat := reg.Catalog()

	got := cat.All()
	got[0].Title = "mutated"
	if again := cat.All(); len(again) != 1 || again[0].Title != "Alpha" {
		t.Fatalf("catalog All must return a copy: %+v", again)
	}
}

type allowAuthorizer struct{}

func (allowAuthorizer) Authorize(context.Context, domain.Caller, string) error {
	return nil
}

type denyAuthorizer struct{}

func (denyAuthorizer) Authorize(context.Context, domain.Caller, string) error {
	return domain.ErrForbidden
}

type guardSource struct {
	err     error
	enabled bool
}

func (s *guardSource) Status(context.Context) (Status, error) {
	return Status{}, nil
}

func (s *guardSource) GetAccountState(context.Context, domain.AccountID) (
	domain.Account,
	node.AccountLimits,
	error,
) {
	return domain.Account{}, node.AccountLimits{}, nil
}

func (s *guardSource) ListLimits(context.Context, domain.AccountID) (
	node.AccountLimits,
	error,
) {
	return node.AccountLimits{}, nil
}

func (s *guardSource) ListAudit(context.Context, int) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *guardSource) ListAuditFiltered(
	context.Context,
	domain.AuditFilter,
	int,
) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *guardSource) CheckOrder(context.Context, domain.OrderProbe) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (s *guardSource) GetOrder(context.Context, string) (domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}

func (s *guardSource) SetMarketDataInstrumentEnabled(
	context.Context,
	string,
	string,
	bool,
) error {
	return nil
}

func (s *guardSource) CommandEnabled(context.Context, string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.enabled, nil
}

func (s *guardSource) SubmitOrderToken(
	context.Context,
	domain.Order,
	string,
) (SubmitOrderTokenResult, error) {
	return SubmitOrderTokenResult{}, nil
}

func (s *guardSource) SubmitDropCopyOrder(
	context.Context, domain.Order,
) (SubmitDropCopyOrderResult, error) {
	return SubmitDropCopyOrderResult{}, nil
}

func (s *guardSource) ConfirmExecution(
	context.Context,
	string,
	string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

func (s *guardSource) CancelOrder(
	context.Context,
	string,
	string,
	string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

func contentContains(content []sdkmcp.Content, needle string) bool {
	for _, c := range content {
		if text, ok := c.(*sdkmcp.TextContent); ok && strings.Contains(text.Text, needle) {
			return true
		}
	}
	return false
}
