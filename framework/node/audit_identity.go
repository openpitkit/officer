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
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

// captureAuditTarget runs under the operation's admission gate, before its
// mutation. The returned entry carries the actual target across the write;
// appending it must never resolve a reusable code again.
func (n *localNode) captureAuditTarget(ctx context.Context, entry store.AuditEntry) (store.AuditEntry, error) {
	if entry.Account != "" {
		if err := domain.ValidateAccountID(entry.Account); err != nil {
			return store.AuditEntry{}, err
		}
		target, found, err := n.realm.GetAccount(ctx, entry.Account)
		if err != nil {
			return store.AuditEntry{}, fmt.Errorf("read audit account target: %w", err)
		}
		if !found {
			return store.AuditEntry{}, domain.ErrNotFound
		}
		entry.AccountIdentity = target.EngineAccountID
	}
	if entry.Group != "" {
		if err := domain.ValidateGroupID(entry.Group); err != nil {
			return store.AuditEntry{}, err
		}
		target, found, err := n.realm.GetGroup(ctx, entry.Group)
		if err != nil {
			return store.AuditEntry{}, fmt.Errorf("read audit group target: %w", err)
		}
		if !found {
			return store.AuditEntry{}, domain.ErrNotFound
		}
		entry.GroupIdentity = target.EngineGroupID
	}
	return entry, nil
}
