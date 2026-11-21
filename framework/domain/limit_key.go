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

package domain

import "fmt"

// KeyedLimit is a barrier whose identity is its LimitKey.
type KeyedLimit interface {
	Key() (LimitKey, error)
}

// StoredLimitKey returns a stored barrier's key. A stored barrier that does
// not form a key is corruption, not caller input. The returned error
// deliberately does not wrap ErrInvalid; callers must not classify it as
// invalid input.
func StoredLimitKey[Limit KeyedLimit](limit Limit) (LimitKey, error) {
	key, err := limit.Key()
	if err != nil {
		// An unknown stored value is corruption, not caller input: discard
		// the parse error's validation chain.
		return LimitKey{}, fmt.Errorf("stored barrier %+v holds an invalid key: %v", limit, err)
	}
	return key, nil
}

// FindLimit returns the first barrier whose stored key equals key. A stored
// barrier that does not form a key returns the StoredLimitKey error.
func FindLimit[L KeyedLimit](limits []L, key LimitKey) (L, bool, error) {
	var zero L
	for _, limit := range limits {
		limitKey, err := StoredLimitKey(limit)
		if err != nil {
			return zero, false, err
		}
		if limitKey == key {
			return limit, true, nil
		}
	}
	return zero, false, nil
}

// LimitKey identifies one limit barrier. Two barriers are the same barrier
// exactly when their keys are ==. The Spot Funds P&L axis is encoded in the
// policy (SpotFundsPnlAxis.Policy), not in a separate field.
// Payload (rate window and count, order-size caps, position ceiling, P&L
// currency and bounds) is never part of the key.
// The zero LimitKey is invalid; code consuming a key must reject it rather
// than treat it as a barrier. Use NewLimitKey to build a non-zero key.
type LimitKey struct {
	policy       string
	mode         RuleMode
	scope        LimitScope
	account      AccountID
	accountGroup string
	asset        string
}

// Policy returns the barrier policy, including the P&L axis where applicable.
func (k LimitKey) Policy() string { return k.policy }

// Mode returns the rule mode for policies identified by LimitPolicyHasMode,
// or an empty mode for other policies.
func (k LimitKey) Mode() RuleMode { return k.mode }

// Scope returns the barrier's scope axis.
func (k LimitKey) Scope() LimitScope { return k.scope }

// Account returns the account axis, or an empty ID when absent.
func (k LimitKey) Account() AccountID { return k.account }

// AccountGroup returns the account-group axis, or an empty string when absent.
func (k LimitKey) AccountGroup() string { return k.accountGroup }

// Asset returns the asset axis, or an empty string when absent.
func (k LimitKey) Asset() string { return k.asset }

// LimitPolicyHasMode reports whether the policy's barrier identity includes a rule mode.
func LimitPolicyHasMode(policy string) bool {
	return policy == PolicyOrderSizeLimit || policy == PolicyRateLimit || policy == PolicyPositionLimit ||
		policy == PolicySpotFundsPnlBoundsKillSwitch ||
		policy == PolicySpotFundsUnrealizedPnlBoundsKillSwitch ||
		policy == PolicySpotFundsTotalPnlBoundsKillSwitch
}

// NewLimitKey validates a barrier's identity, checking mode before scope and
// axes. Payload validation belongs to the limit, not to its key.
func NewLimitKey(
	policy string, mode RuleMode, scope LimitScope, account AccountID,
	accountGroup string, asset string,
) (LimitKey, error) {
	if LimitPolicyHasMode(policy) {
		if err := ValidateRuleMode(mode); err != nil {
			return LimitKey{}, err
		}
	} else if mode != "" {
		return LimitKey{}, NewValidationError("/mode", "unsupported_value", "policy does not support a rule mode")
	}
	if err := ValidateLimitScopeAndAxes(
		policy, scope, account, asset, accountGroup,
	); err != nil {
		return LimitKey{}, err
	}
	return LimitKey{
		policy: policy, mode: mode, scope: scope, account: account,
		accountGroup: accountGroup, asset: asset,
	}, nil
}
