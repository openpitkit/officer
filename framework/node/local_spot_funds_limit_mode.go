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

// storedSpotFundsLimitModeRow reads the meta-policy row: mode is the mode in
// force - the stored one, or Enforce, the documented default
// (domain.SettingKeySpotFundsLimitMode), while none is stored - and stored
// reports whether a row is stored.
func (n *localNode) storedSpotFundsLimitModeRow(
	ctx context.Context,
) (mode domain.SpotFundsLimitMode, stored bool, err error) {
	value, ok, err := n.realm.GetSetting(ctx, domain.SettingKeySpotFundsLimitMode)
	if err != nil {
		return "", false, err
	}
	if !ok {
		return domain.SpotFundsLimitModeEnforce, false, nil
	}
	mode, err = domain.ParseSpotFundsLimitMode(value)
	if err != nil {
		// An unknown stored value is corruption, not caller input: discard
		// the parse error's validation chain.
		return "", false, fmt.Errorf(
			"setting %q holds invalid stored value %q",
			domain.SettingKeySpotFundsLimitMode, value,
		)
	}
	return mode, true, nil
}

// storedSpotFundsLimitMode reads the mode in force (storedSpotFundsLimitModeRow).
func (n *localNode) storedSpotFundsLimitMode(ctx context.Context) (domain.SpotFundsLimitMode, error) {
	mode, _, err := n.storedSpotFundsLimitModeRow(ctx)
	return mode, err
}

func (n *localNode) SpotFundsLimitMode(ctx context.Context) (domain.SpotFundsLimitModeState, error) {
	mode, stored, err := n.storedSpotFundsLimitModeRow(ctx)
	if err != nil {
		return domain.SpotFundsLimitModeState{}, fmt.Errorf("read spot funds limit mode: %w", err)
	}
	accounts, err := n.realm.ListAccounts(ctx)
	if err != nil {
		return domain.SpotFundsLimitModeState{}, fmt.Errorf("list accounts for spot funds limit mode: %w", err)
	}
	state := domain.SpotFundsLimitModeState{Mode: mode, Stored: stored}
	var candidates []domain.AccountID
	for _, account := range accounts {
		if account.EffectiveCurrency == "" {
			candidates = append(candidates, account.Code)
		}
	}
	if len(candidates) == 0 {
		return state, nil
	}
	blockers, err := n.realm.ListAccountsBlockingCurrencyChange(ctx, candidates)
	if err != nil {
		return domain.SpotFundsLimitModeState{}, fmt.Errorf("list currency change blockers for spot funds limit mode: %w", err)
	}
	currencyValuedLimits := make(map[domain.AccountID]struct{}, len(blockers))
	for _, blocker := range blockers {
		if blocker.CurrencyValuedLimit {
			currencyValuedLimits[blocker.Account] = struct{}{}
		}
	}
	for _, account := range candidates {
		if _, ok := currencyValuedLimits[account]; ok {
			state.AccountsWithoutCurrency = append(state.AccountsWithoutCurrency, account)
		}
	}
	return state, nil
}

func (n *localNode) SetSpotFundsLimitMode(
	ctx context.Context, mode domain.SpotFundsLimitMode, caller domain.Caller,
) error {
	if err := domain.ValidateSpotFundsLimitMode(mode); err != nil {
		return err
	}
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return err
	}
	defer n.endLivePolicyConfiguration()

	prev, err := n.storedSpotFundsLimitMode(ctx)
	if err != nil {
		return fmt.Errorf("set spot funds limit mode: %w", err)
	}
	if caller.TightenOnly() {
		if err := mode.NoLooserThan(prev); err != nil {
			return fmt.Errorf("set spot funds limit mode: %w", err)
		}
	}
	if err := n.applySpotFundsLimitMode(ctx, caller, &mode, store.AuditEntry{
		Action: domain.AuditActionSetLimit,
		Detail: setSpotFundsLimitModeDetail(mode),
	}); err != nil {
		return fmt.Errorf("set spot funds limit mode: %w", err)
	}
	return nil
}

// deleteSpotFundsLimitMode removes the Spot Funds limit-mode meta-policy that
// target addresses and applies Enforce, the mode an absent setting means. The
// removal restores Enforce, which only tightens, so a tighten-only caller may
// make it. The meta-policy has no engine barrier, so nothing is rebuilt or
// reconfigured. An absent setting is refused with domain.ErrNotFound, as the
// store refuses the deletion of an absent barrier.
func (n *localNode) deleteSpotFundsLimitMode(
	ctx context.Context, target LimitTarget, caller domain.Caller,
) error {
	if err := domain.ValidateLimitScopeAndAxes(
		target.Policy, target.Scope, target.Account, target.Asset, target.AccountGroup,
	); err != nil {
		return err
	}
	if err := n.beginLivePolicyConfiguration(); err != nil {
		return err
	}
	defer n.endLivePolicyConfiguration()

	if err := n.applySpotFundsLimitMode(ctx, caller, nil, store.AuditEntry{
		Action:  domain.AuditActionDeleteLimit,
		Account: target.Account,
		Detail:  deleteLimitDetail(target),
	}); err != nil {
		return fmt.Errorf("delete spot funds limit mode: %w", err)
	}
	return nil
}

// applySpotFundsLimitMode stores next, or deletes the stored mode when next is
// nil, applies the mode that leaves in force to the live engine, and audits
// entry. When the engine refuses, it restores the stored state it found - the
// raw value, or no value - so a failed change leaves no row it did not find.
// The caller holds the live-policy configuration gate.
func (n *localNode) applySpotFundsLimitMode(
	ctx context.Context, caller domain.Caller,
	next *domain.SpotFundsLimitMode, entry store.AuditEntry,
) error {
	key := domain.SettingKeySpotFundsLimitMode
	prev, hadPrev, err := n.realm.GetSetting(ctx, key)
	if err != nil {
		return err
	}
	trackOnly := false
	if next == nil {
		err = n.realm.DeleteSetting(ctx, key)
	} else {
		trackOnly = *next == domain.SpotFundsLimitModeTrackOnly
		err = n.realm.SetSetting(ctx, key, string(*next))
	}
	if err != nil {
		return err
	}
	if err := n.currentEngine().SetSpotFundsTrackOnly(ctx, trackOnly); err != nil {
		restoreCtx := context.WithoutCancel(ctx)
		var revertErr error
		if hadPrev {
			revertErr = n.realm.SetSetting(restoreCtx, key, prev)
		} else {
			revertErr = n.realm.DeleteSetting(restoreCtx, key)
		}
		if revertErr != nil {
			return n.fatalPostEngineAuditByCode(
				"revert spot funds limit mode", "policy", domain.PolicySpotFundsLimitMode,
				fmt.Errorf("revert spot funds limit mode: %w", revertErr),
			)
		}
		return err
	}
	if err := n.audit(context.WithoutCancel(ctx), caller, entry); err != nil {
		return n.fatalPostEngineAuditByCode(
			"audit spot funds limit mode", "policy", domain.PolicySpotFundsLimitMode,
			fmt.Errorf("audit %s: %w", entry.Action, err),
		)
	}
	return nil
}
