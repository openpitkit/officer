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

package domain

// AccountBlockSource names the tier that produced an account's effective block.
type AccountBlockSource string

const (
	// AccountBlockSourceNone means no tier blocks the account.
	AccountBlockSourceNone AccountBlockSource = "none"
	// AccountBlockSourceAccount means the account carries its own latched block.
	AccountBlockSourceAccount AccountBlockSource = "account"
	// AccountBlockSourceGroup means the group the account currently belongs to
	// is blocked.
	AccountBlockSourceGroup AccountBlockSource = "group"
)

// AccountBlockState is an account's effective kill-switch state: the account's
// own latched block joined with the block of the group it currently belongs to.
//
// The engine tests the account's own block first and then resolves the
// account's group live, rejecting every member of a blocked group before any
// policy runs. A surface that reports only the account's own flag therefore
// calls a member of a blocked group tradeable while the engine refuses each of
// its orders.
//
// The two tiers are independent stores: unblocking the account leaves a group
// block in force, and unblocking the group leaves an individually blocked
// account blocked. The account's own block wins reason attribution, mirroring
// the engine.
type AccountBlockState struct {
	// Reason is the effective block reason: the account's own reason when the
	// account itself is blocked, otherwise the group's. Empty when nothing
	// blocks the account.
	Reason string
	// AccountReason is the reason latched on the account itself.
	AccountReason string
	// GroupReason is the reason latched on the account's current group.
	GroupReason string
	// Source names the tier the effective block came from.
	Source AccountBlockSource
	// Blocked is the effective answer to "can this account trade right now".
	Blocked bool
	// AccountBlocked is the account's own latched block.
	AccountBlocked bool
	// GroupBlocked reports that the account's current group is blocked.
	GroupBlocked bool
}

// ResolveAccountBlock joins an account's own block with the block of group,
// which must be the group the account currently belongs to. A zero group (the
// account belongs to none) contributes no block.
func ResolveAccountBlock(account Account, group AccountGroup) AccountBlockState {
	state := AccountBlockState{
		AccountReason:  account.BlockReason,
		AccountBlocked: account.Blocked,
		Source:         AccountBlockSourceNone,
	}
	// An account with no group must not inherit the reserved default group,
	// which is stored under the empty code and is never blockable anyway.
	if group.Code != "" && group.Blocked {
		state.GroupBlocked = true
		state.GroupReason = group.BlockReason
	}
	switch {
	case state.AccountBlocked:
		state.Blocked = true
		state.Source = AccountBlockSourceAccount
		state.Reason = state.AccountReason
	case state.GroupBlocked:
		state.Blocked = true
		state.Source = AccountBlockSourceGroup
		state.Reason = state.GroupReason
	}
	return state
}

// GroupBlockIndex indexes account groups by their public code so an account
// read path can join the group tier of the block onto many accounts without a
// lookup per row.
type GroupBlockIndex map[string]AccountGroup

// NewGroupBlockIndex indexes groups by code. The reserved default group, stored
// under the empty code, is skipped: accounts with no group must not resolve
// through it.
func NewGroupBlockIndex(groups []AccountGroup) GroupBlockIndex {
	index := make(GroupBlockIndex, len(groups))
	for _, group := range groups {
		if group.Code == "" {
			continue
		}
		index[group.Code] = group
	}
	return index
}

// Resolve joins account with the indexed state of its group. An account whose
// group is absent from the index resolves to its own block alone.
func (index GroupBlockIndex) Resolve(account Account) AccountBlockState {
	return ResolveAccountBlock(account, index[account.GroupCode])
}
