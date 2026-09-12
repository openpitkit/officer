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

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"go.openpit.dev/officer/app/development"
	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/mcp/tools"
)

type principalStoreSource struct {
	*fakeSource
	service *backend.Service
}

func (s *principalStoreSource) SubmitDropCopyOrder(
	ctx context.Context,
	o domain.Order,
	missing domain.MissingAccountPolicy,
) (SubmitDropCopyOrderResult, error) {
	submitted, err := s.service.SubmitDropCopyOrder(ctx, o, missing)
	if err != nil {
		return SubmitDropCopyOrderResult{}, err
	}
	return SubmitDropCopyOrderResult{
		OrderExternalID: submitted.ExternalID.String(),
		Status:          submitted.Status,
	}, nil
}

func TestMCPGuardPersistsOperatorPrincipal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	runtimeLibrary := os.Getenv("OPENPIT_RUNTIME_LIBRARY_PATH")
	if runtimeLibrary == "" {
		t.Fatal("OPENPIT_RUNTIME_LIBRARY_PATH is required; use the local SDK development gate")
	}
	target, err := development.NewNode(ctx, development.Config{
		Realm:              domain.DefaultRealm,
		DatabasePath:       filepath.Join(t.TempDir(), "mcp-principal.db"),
		RuntimeLibraryPath: runtimeLibrary,
		FatalShutdownHook:  func(error) {},
	})
	if err != nil {
		t.Fatalf("development.NewNode: %v", err)
	}
	t.Cleanup(func() { _ = target.Close() })
	for _, code := range []string{"AAPL", "USD"} {
		if _, err := target.CreateAsset(
			ctx, domain.Asset{Code: code}, domain.Caller{Source: domain.SourceSystem},
		); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	router, err := node.NewLocalRouter(target)
	if err != nil {
		t.Fatalf("NewLocalRouter: %v", err)
	}
	source := &principalStoreSource{
		fakeSource: &fakeSource{},
		service:    backend.New(router, nil, nil),
	}
	guarded := frameworkmcp.Guard(
		testDescriptor("submit_drop_copy_order"),
		frameworkmcp.RegisterDeps{Source: source},
		tools.SubmitDropCopyOrderHandler(source),
	)
	result, err := guarded(ctx, nil,
		&sdkmcp.CallToolParamsFor[tools.SubmitDropCopyOrderInput]{
			Arguments: tools.SubmitDropCopyOrderInput{
				Account: "mcp-account", BaseAsset: "AAPL", QuoteAsset: "USD",
				Side: "buy", AmountKind: "quantity", AmountValue: "1",
				Price: "150", ExternalID: "mcp-principal-order",
				MissingAccount: "create",
			},
		})
	if err != nil || result.IsError {
		t.Fatalf("guarded submit = %+v, error = %v", result, err)
	}
	detail, err := target.GetOrder(ctx, domain.ExternalID("mcp-principal-order"))
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if detail.Order.Principal != domain.PrincipalOperator {
		t.Fatalf(
			"stored principal = %q, want %q",
			detail.Order.Principal,
			domain.PrincipalOperator,
		)
	}
}
