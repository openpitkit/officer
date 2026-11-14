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

package node

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/web/httpapi/dto"
)

func (n *localNode) GetTestExecutionPolicy(ctx context.Context, connection domain.ExternalID) (domain.TestExecutionPolicyState, error) {
	state, err := n.realm.GetTestExecutionPolicy(ctx, connection)
	if err != nil {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("get test execution policy: %w", err)
	}
	return state, nil
}

func (n *localNode) ReplaceTestExecutionPolicy(ctx context.Context, connection domain.ExternalID, policy domain.TestExecutionPolicy, caller domain.Caller) (domain.TestExecutionPolicyState, error) {
	if err := n.beginMutation(); err != nil {
		return domain.TestExecutionPolicyState{}, err
	}
	defer n.endMutation()

	oldState, newState, err := n.realm.ReplaceTestExecutionPolicy(ctx, connection, policy)
	if err != nil {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("replace test execution policy: %w", err)
	}
	oldRules, err := json.Marshal(dto.ToTestExecutionPolicyStateDTO(connection, oldState).Rules)
	if err != nil {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("encode old test execution rules: %w", err)
	}
	newRules, err := json.Marshal(dto.ToTestExecutionPolicyStateDTO(connection, newState).Rules)
	if err != nil {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("encode new test execution rules: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetTrading,
		Detail: domain.NewAuditDetail(domain.AuditDetailSetTestExecutionPolicy, domain.AuditDetailParams{
			"connection":  connection.String(),
			"oldRevision": strconv.FormatUint(oldState.Revision, 10),
			"newRevision": strconv.FormatUint(newState.Revision, 10),
		}, domain.AuditDetailField{Name: "oldRules", Value: string(oldRules)}, domain.AuditDetailField{Name: "newRules", Value: string(newRules)}),
	}); err != nil {
		return domain.TestExecutionPolicyState{}, fmt.Errorf("audit replace test execution policy: %w", err)
	}
	return newState, nil
}
