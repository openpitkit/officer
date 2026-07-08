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

package node

import (
	"context"
	"errors"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

// --- groups -----------------------------------------------------------------

// CreateGroup persists a new account group, rebuilds the live engine so the
// group enters the resolver, and audits the action. Like CreateAccount, the
// resolver has no incremental group registration, so a runtime-created group is
// unknown to the engine — a later group move or group-scoped barrier would
// reject as "unknown group" — until the engine is rebuilt from the store. The
// store assigns the engine group id and returns the populated group.
func (n *localNode) CreateGroup(
	ctx context.Context, group domain.AccountGroup, caller domain.Caller,
) (domain.AccountGroup, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.AccountGroup{}, err
	}
	defer n.endEngineRestart()

	if group.Currency != "" {
		if _, err := n.ensureAutoCreatedAsset(
			ctx, group.Currency, "create group currency", caller,
		); err != nil {
			return domain.AccountGroup{}, err
		}
	}

	created, err := n.realm.CreateGroup(ctx, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("create group: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("rebuild engine after group create: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionCreateGroup,
		Detail: fmt.Sprintf("create group %s", group.Code),
	}); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("audit create group: %w", err)
	}
	return created, nil
}

// ListGroups returns every persisted group.
func (n *localNode) ListGroups(ctx context.Context) ([]domain.AccountGroup, error) {
	groups, err := n.realm.ListGroups(ctx)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	return groups, nil
}

// ListGroupRows returns persisted groups matching filter.
func (n *localNode) ListGroupRows(
	ctx context.Context, filter store.GroupListFilter,
) (store.GroupListPage, error) {
	page, err := n.realm.ListGroupRows(ctx, filter)
	if err != nil {
		return store.GroupListPage{}, fmt.Errorf("list group rows: %w", err)
	}
	return page, nil
}

// GetGroup returns the group and its member accounts. The bool is false when no
// such group exists.
func (n *localNode) GetGroup(
	ctx context.Context, code string,
) (domain.AccountGroup, []domain.Account, bool, error) {
	group, ok, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("get group: %w", err)
	}
	if !ok {
		return domain.AccountGroup{}, nil, false, nil
	}
	members, err := n.realm.ListGroupAccounts(ctx, code)
	if err != nil {
		return domain.AccountGroup{}, nil, false, fmt.Errorf("list group accounts: %w", err)
	}
	return group, members, true, nil
}

// SetGroupNotes replaces a group's notes in the store and audits the action.
// If no group record exists yet (e.g. the group is known only via account
// membership), a default record is created first so the update succeeds.
//
// Auto-creating that record must also register the group in the engine, or the
// live resolver would not know it and a later group move into this code would
// reject as "unknown group". The ensure runs under the exclusive engine-restart
// gate, which takes the mutation lock internally, so it must run before
// beginMutation (never while mutate is held) or it self-deadlocks; the notes
// write then runs under beginMutation and finds the row the ensure created.
func (n *localNode) SetGroupNotes(
	ctx context.Context, code, notes string, caller domain.Caller,
) error {
	if err := n.ensureGroupRegisteredExclusive(ctx, code); err != nil {
		return fmt.Errorf("ensure group for set notes: %w", err)
	}

	if err := n.beginMutation(); err != nil {
		return err
	}
	defer n.endMutation()

	if err := n.realm.SetGroupNotes(ctx, code, notes); err != nil {
		return fmt.Errorf("set group notes: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionSetGroupNotes,
		Detail: fmt.Sprintf("set notes group %s", code),
	}); err != nil {
		return fmt.Errorf("audit set group notes: %w", err)
	}
	return nil
}

// UpdateGroup replaces a group's public code and title, rebuilds the engine
// resolver, and audits the change.
func (n *localNode) UpdateGroup(
	ctx context.Context,
	oldCode string,
	group domain.AccountGroup,
	caller domain.Caller,
) (domain.AccountGroup, error) {
	if err := n.beginEngineRestart(); err != nil {
		return domain.AccountGroup{}, err
	}
	defer n.endEngineRestart()

	prev, ok, err := n.realm.GetGroup(ctx, oldCode)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("read group for update: %w", err)
	}
	if !ok {
		return domain.AccountGroup{},
			fmt.Errorf("group %q: %w", oldCode, domain.ErrNotFound)
	}
	updated, err := n.realm.UpdateGroup(ctx, oldCode, group)
	if err != nil {
		return domain.AccountGroup{}, fmt.Errorf("update group: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return domain.AccountGroup{},
			fmt.Errorf("rebuild engine after group update: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionUpdateGroup,
		Detail: fmt.Sprintf(
			"update group %s -> %s",
			prev.Code,
			updated.Code,
		),
	}); err != nil {
		return domain.AccountGroup{}, fmt.Errorf("audit update group: %w", err)
	}
	return updated, nil
}

// SetGroupBlocked blocks or unblocks the group in the store, then the engine,
// reverts the store on engine failure, and audits the action. If no group
// record exists yet (e.g. the group is known only via account membership), a
// default record is created first so the operation succeeds.
//
// The block spans every member account, so a group has no single account lane
// to serialize on; instead the whole op runs under the exclusive engine-restart
// gate, which quiesces every account lane. That gate serves two ends: a missing
// group is auto-created and the engine rebuilt from the store so the resolver
// knows it before the block runs, and the group effect is ordered against every
// member-account fill, report, and block. Concurrent engine-restart-class admin
// requests are rejected with domain.ErrEngineRestarting; in-lane requests (fills,
// reports, account blocks) block on the gate and proceed once the group block
// completes.
func (n *localNode) SetGroupBlocked(
	ctx context.Context, code string, blocked bool, reason string, caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	prev, existed, err := n.realm.GetGroup(ctx, code)
	if err != nil {
		return fmt.Errorf("read group for block: %w", err)
	}
	if !existed {
		if err := n.ensureGroupRecordLocked(ctx, code); err != nil {
			return fmt.Errorf("ensure group for block: %w", err)
		}
		// The auto-created record is unknown to the resolver until the engine is
		// rebuilt from the store, so rebuild before the block runs.
		if err := n.rebuildEngineFromStore(ctx); err != nil {
			return fmt.Errorf("rebuild engine after group ensure: %w", err)
		}
	}

	if err := n.realm.SetGroupBlocked(ctx, code, blocked, reason); err != nil {
		return fmt.Errorf("set group blocked: %w", err)
	}

	applyErr := n.engine.RunGroupSynchronized(ctx, code, func(lane engine.GroupLane) error {
		return n.applyGroupBlock(ctx, lane, code, blocked, reason)
	})
	if applyErr != nil {
		// Best-effort revert; the caller already surfaces the primary error.
		_ = n.realm.SetGroupBlocked(ctx, code, prev.Blocked, prev.BlockReason)
		return fmt.Errorf("apply group block: %w", applyErr)
	}

	action := domain.AuditActionBlockGroup
	detail := fmt.Sprintf("block group %s", code)
	if !blocked {
		action = domain.AuditActionUnblockGroup
		detail = fmt.Sprintf("unblock group %s", code)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: action,
		Detail: detail,
	}); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit group block", "group", code,
			fmt.Errorf("audit group block: %w", err),
		)
	}
	return nil
}

// ensureGroupRecord creates an account_groups record for code if one does not
// already exist. ErrAlreadyExists is treated as success so the call is
// idempotent.
func (n *localNode) ensureGroupRecordLocked(ctx context.Context, code string) error {
	_, err := n.realm.CreateGroup(ctx, domain.AccountGroup{Code: code})
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		return err
	}
	return nil
}

// applyGroupBlock applies the desired blocked state for a group to the engine
// through the supplied group view. SetGroupBlocked passes the live engine under
// the exclusive restart gate; the business CSV import passes a group lane.
func (n *localNode) applyGroupBlock(
	ctx context.Context, lane engine.GroupLane, code string, blocked bool, reason string,
) error {
	if blocked {
		return lane.BlockGroup(ctx, code, reason)
	}
	return lane.UnblockGroup(ctx, code)
}

// DeleteGroup removes the group from the store, rebuilds the live engine from
// the resulting snapshot, and audits the action.
func (n *localNode) DeleteGroup(
	ctx context.Context, code string, caller domain.Caller,
) error {
	if err := n.beginEngineRestart(); err != nil {
		return err
	}
	defer n.endEngineRestart()

	if err := n.guardGroupDeleteCurrencyChange(ctx, code); err != nil {
		return err
	}
	if err := n.realm.DeleteGroup(ctx, code); err != nil {
		return fmt.Errorf("delete group: %w", err)
	}
	if err := n.rebuildEngineFromStore(ctx); err != nil {
		return fmt.Errorf("rebuild engine after group delete: %w", err)
	}
	if err := n.audit(ctx, caller, store.AuditEntry{
		Action: domain.AuditActionDeleteGroup,
		Detail: fmt.Sprintf("delete group %s", code),
	}); err != nil {
		return fmt.Errorf("audit delete group: %w", err)
	}
	return nil
}
