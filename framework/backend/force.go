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

package backend

import (
	"context"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
)

// ForceAuthorizer decides whether a surface caller may force an operation.
// Its consumer-side interface matches httpx.Authorizer without importing
// httpapi, which imports backend and would create an import cycle.
// Authorize receives ForceDeleteAccount, ForceDeleteGroup, ForceDeleteAsset,
// ForceDeleteAssetClass or ForceExecutionReport only when force is set and
// the caller is not domain.SourceSystem. Without an option, New allows all.
// Any non-nil error refuses the operation, is recorded with its text in the
// audit log, and is returned to the surface unchanged unless auditing fails.
// An error wrapping domain.ErrForbidden maps to REST 403 / MCP forbidden
// through the existing DescribeErr mapping. Allowing does not guarantee
// success: the node's own checks still run.
type ForceAuthorizer interface {
	Authorize(ctx context.Context, caller domain.Caller, identifier string) error
}

// ForceDeleteAccount, ForceDeleteGroup, ForceDeleteAsset, ForceDeleteAssetClass
// and ForceExecutionReport identify forced operations. Their force: prefix
// distinguishes them from dotted REST route IDs and snake_case MCP tool names.
const (
	ForceDeleteAccount    = "force:" + string(domain.AuditActionDeleteAccount)
	ForceDeleteGroup      = "force:" + string(domain.AuditActionDeleteGroup)
	ForceDeleteAsset      = "force:" + string(domain.AuditActionDeleteAsset)
	ForceDeleteAssetClass = "force:" + string(domain.AuditActionDeleteAssetClass)
	ForceExecutionReport  = "force:" + string(domain.AuditActionExecutionReport)
)

type allowForce struct{}

func (allowForce) Authorize(context.Context, domain.Caller, string) error { return nil }

// authorizeForce governs surface callers, as does the route authorizer it
// mirrors. That authorizer has never been asked about a system caller, so
// Officer's own processing (domain.SourceSystem) is not asked either.
func (s *Service) authorizeForce(
	ctx context.Context, caller domain.Caller, identifier string,
	entry store.AuditEntry, code domain.AuditDetailCode, params domain.AuditDetailParams,
) error {
	if caller.Source == domain.SourceSystem {
		return nil
	}
	refusal := s.forceAuthorizer.Authorize(ctx, caller, identifier)
	if refusal == nil {
		return nil
	}
	return node.AuditRefusal(ctx, s.node, caller, entry, code, params, refusal, identifier)
}
