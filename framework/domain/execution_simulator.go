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

import (
	"errors"
	"fmt"
)

// ExecutionPolicyProviderLookup resolves the capability declared by a registered provider.
type ExecutionPolicyProviderLookup func(string) (bool, error)

// ProviderSupportsExecutionPolicy resolves the registered execution-policy capability.
// Errors from the provider lookup are returned unchanged.
func ProviderSupportsExecutionPolicy(lookup ExecutionPolicyProviderLookup, provider string) (bool, error) {
	if lookup == nil {
		return false, fmt.Errorf("provider %q has no execution policy capability lookup: %w", provider, ErrTradingProviderUnavailable)
	}
	return lookup(provider)
}

// ValidateTradingAccessModes rejects unknown modes without restricting their combination.
func ValidateTradingAccessModes(account AccountID, modes []TradingMode) error {
	for _, mode := range modes {
		if !TradingModeSupported(mode) {
			return fmt.Errorf("trading connection mode %q for account %s: %w", mode, account, ErrInvalid)
		}
	}
	return nil
}

var (
	// ErrTradingProviderUnavailable means a provider is not registered or cannot be resolved.
	ErrTradingProviderUnavailable = registerSentinel(errors.New("trading provider unavailable"))
	// ErrExecutionPolicyUnsupported means the provider has no execution-policy capability.
	ErrExecutionPolicyUnsupported = registerSentinel(errors.New("execution policy is not supported by this trading provider"))
)

// TestExecutionMode selects execution, rejection, split fills, or resting behavior.
type TestExecutionMode string

// TestPriceVariant selects the normal, best, or worst test execution price.
type TestPriceVariant string

// TestExecutionWait describes a finite or infinite rest interval.
type TestExecutionWait struct {
	Kind    string
	Seconds *int64
}

// TestExecutionRule assigns test execution behavior to an instrument mask.
type TestExecutionRule struct {
	Mask         InstrumentMask
	Mode         TestExecutionMode
	PriceVariant TestPriceVariant
	Fills        *int
	Wait         *TestExecutionWait
}

// TestExecutionPolicy holds the rules for a test trading connection.
type TestExecutionPolicy struct {
	Rules []TestExecutionRule
}

// TestExecutionPolicyState pairs a policy snapshot with its revision.
type TestExecutionPolicyState struct {
	Revision uint64
	Policy   TestExecutionPolicy
}

// ValidateTestExecutionPolicy checks each rule without reordering or resolving overlaps.
func ValidateTestExecutionPolicy(policy TestExecutionPolicy) error {
	if policy.Rules == nil {
		return NewValidationError("/rules", "required", "rules are required")
	}
	for i, rule := range policy.Rules {
		pointer := fmt.Sprintf("/rules/%d", i)
		if _, err := ParseInstrumentMask(rule.Mask.BaseAsset(), rule.Mask.QuoteAsset()); err != nil {
			return WithValidationPointer(pointer+"/mask"+ValidationPointer(err), err)
		}
		invalid := func(field, constraint string) error {
			return NewValidationError(pointer+"/"+field, constraint, "invalid test execution "+field+": "+constraint)
		}
		switch rule.Mode {
		case "execute", "reject_even":
			if rule.Fills != nil {
				return invalid("fills", "forbidden")
			}
			if rule.Wait != nil {
				return invalid("wait", "forbidden")
			}
		case "split":
			if rule.Fills == nil {
				return invalid("fills", "required")
			}
			if *rule.Fills != 2 && *rule.Fills != 3 {
				return invalid("fills", "enum")
			}
			if rule.Wait != nil {
				return invalid("wait", "forbidden")
			}
		case "rest":
			if rule.Fills != nil {
				return invalid("fills", "forbidden")
			}
			if rule.Wait == nil {
				return invalid("wait", "required")
			}
			switch rule.Wait.Kind {
			case "finite":
				if rule.Wait.Seconds == nil {
					return invalid("wait/seconds", "required")
				}
				if *rule.Wait.Seconds < 1 || *rule.Wait.Seconds > 9223372036 {
					return invalid("wait/seconds", "range")
				}
			case "infinite":
				if rule.Wait.Seconds != nil {
					return invalid("wait/seconds", "forbidden")
				}
				if rule.PriceVariant != "" {
					return invalid("priceVariant", "forbidden")
				}
				continue
			case "":
				return invalid("wait/kind", "required")
			default:
				return invalid("wait/kind", "enum")
			}
		case "":
			return invalid("mode", "required")
		default:
			return invalid("mode", "enum")
		}
		switch rule.PriceVariant {
		case "normal", "best", "worst":
		case "":
			return invalid("priceVariant", "required")
		default:
			return invalid("priceVariant", "enum")
		}
	}
	return nil
}
