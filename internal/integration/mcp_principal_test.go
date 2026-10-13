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
	"testing"

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

type principalStoreSource struct {
	*fakeSource
	service *backend.Service
}

func (s *principalStoreSource) SubmitDropCopyOrder(
	ctx context.Context,
	o domain.Order,
	missing domain.MissingAccountPolicy,
) (frameworkmcp.SubmitDropCopyOrderResult, error) {
	submitted, err := s.service.SubmitDropCopyOrder(ctx, o, missing)
	if err != nil {
		return frameworkmcp.SubmitDropCopyOrderResult{}, err
	}
	return frameworkmcp.SubmitDropCopyOrderResult{
		OrderExternalID: submitted.ExternalID.String(),
		Status:          submitted.Status,
	}, nil
}

func TestMCPGuardPersistsOperatorPrincipal(t *testing.T) {
	t.Parallel()
	ctx := systemCtx()
	target, _ := newLocalNode(t)
	for _, code := range []string{"AAPL", "USD"} {
		if _, err := target.CreateAsset(
			ctx, domain.Asset{Code: code}, domain.Caller{Source: domain.SourceSystem},
		); err != nil {
			t.Fatalf("CreateAsset(%s): %v", code, err)
		}
	}
	service := newOfficerService(t, target, nil, nil)
	source := &principalStoreSource{
		fakeSource: &fakeSource{},
		service:    service,
	}
	result := callSubmitDropCopyOrder(t, source, submitDropCopyOrderInput{
		Account: "mcp-account", BaseAsset: "AAPL", QuoteAsset: "USD",
		Side: "buy", AmountKind: "quantity", AmountValue: "1",
		Price: "150", ExternalID: "mcp-principal-order",
		MissingAccount: "create",
	})
	if result.IsError {
		t.Fatalf("guarded submit = %+v", result)
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
