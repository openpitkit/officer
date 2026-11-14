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

package dto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	strictjson "github.com/segmentio/encoding/json"

	"go.openpit.dev/officer/framework/domain"
)

// TestExecutionWaitDTO describes a finite or infinite rest interval on the wire.
type TestExecutionWaitDTO struct {
	Kind    string `json:"kind"`
	Seconds *int64 `json:"seconds,omitempty"`
}

// TestExecutionRuleDTO describes execution behavior for an instrument mask on the wire.
type TestExecutionRuleDTO struct {
	Mask         InstrumentMaskDTO     `json:"mask"`
	Mode         string                `json:"mode"`
	PriceVariant *string               `json:"priceVariant,omitempty"`
	Fills        *int                  `json:"fills,omitempty"`
	Wait         *TestExecutionWaitDTO `json:"wait,omitempty"`
}

// TestExecutionPolicyDTO is the shared REST/MCP test execution policy payload.
type TestExecutionPolicyDTO struct {
	Rules []TestExecutionRuleDTO `json:"rules"`
}

// UnmarshalJSON enforces exact field names and rejects null optional members.
func (dto *TestExecutionPolicyDTO) UnmarshalJSON(data []byte) error {
	type policyDTO TestExecutionPolicyDTO
	var decoded policyDTO
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	// Keep standard type-error offsets for the shared REST field-error mapper.
	strictDecoder := strictjson.NewDecoder(bytes.NewReader(data))
	strictDecoder.DisallowUnknownFields()
	strictDecoder.DontMatchCaseInsensitiveStructFields()
	if err := strictDecoder.Decode(&decoded); err != nil {
		return err
	}
	var presence struct {
		Rules []struct {
			PriceVariant json.RawMessage `json:"priceVariant"`
			Fills        json.RawMessage `json:"fills"`
			Wait         json.RawMessage `json:"wait"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(data, &presence); err != nil {
		return err
	}
	for i, rule := range presence.Rules {
		pointer := fmt.Sprintf("/rules/%d/", i)
		for _, field := range []struct {
			name  string
			value json.RawMessage
		}{
			{"priceVariant", rule.PriceVariant}, {"fills", rule.Fills}, {"wait", rule.Wait},
		} {
			if bytes.Equal(bytes.TrimSpace(field.value), []byte("null")) {
				return domain.NewValidationError(pointer+field.name, "type", field.name+" must not be null")
			}
		}
		if len(rule.Wait) != 0 {
			var wait struct {
				Seconds json.RawMessage `json:"seconds"`
			}
			if err := json.Unmarshal(rule.Wait, &wait); err != nil {
				return err
			}
			if bytes.Equal(bytes.TrimSpace(wait.Seconds), []byte("null")) {
				return domain.NewValidationError(pointer+"wait/seconds", "type", "seconds must not be null")
			}
		}
	}
	*dto = TestExecutionPolicyDTO(decoded)
	return nil
}

// TestExecutionPolicyStateDTO identifies a connection's policy and its revision on the wire.
type TestExecutionPolicyStateDTO struct {
	Connection string `json:"connection"`
	Revision   string `json:"revision"`
	TestExecutionPolicyDTO
}

// UnmarshalJSON decodes snapshot metadata without promoting the policy decoder.
func (dto *TestExecutionPolicyStateDTO) UnmarshalJSON(data []byte) error {
	var decoded struct {
		Connection string                 `json:"connection"`
		Revision   string                 `json:"revision"`
		Rules      []TestExecutionRuleDTO `json:"rules"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*dto = TestExecutionPolicyStateDTO{Connection: decoded.Connection, Revision: decoded.Revision, TestExecutionPolicyDTO: TestExecutionPolicyDTO{Rules: decoded.Rules}}
	return nil
}

// FromTestExecutionPolicyDTO converts and validates the shared REST/MCP policy.
func FromTestExecutionPolicyDTO(dto TestExecutionPolicyDTO) (domain.TestExecutionPolicy, error) {
	if dto.Rules == nil {
		return domain.TestExecutionPolicy{}, domain.NewValidationError("/rules", "required", "rules are required")
	}
	policy := domain.TestExecutionPolicy{Rules: make([]domain.TestExecutionRule, 0, len(dto.Rules))}
	for i, rule := range dto.Rules {
		mask, err := FromInstrumentMaskDTO(rule.Mask)
		if err != nil {
			return domain.TestExecutionPolicy{}, domain.WithValidationPointer(fmt.Sprintf("/rules/%d/mask", i)+domain.ValidationPointer(err), err)
		}
		// Presence matters even for an explicitly empty variant on infinite rest.
		if rule.Mode == "rest" && rule.Wait != nil && rule.Wait.Kind == "infinite" && rule.PriceVariant != nil {
			return domain.TestExecutionPolicy{}, domain.NewValidationError(fmt.Sprintf("/rules/%d/priceVariant", i), "forbidden", "infinite rest forbids priceVariant")
		}
		converted := domain.TestExecutionRule{Mask: mask, Mode: domain.TestExecutionMode(rule.Mode)}
		if rule.Fills != nil {
			fills := *rule.Fills
			converted.Fills = &fills
		}
		if rule.PriceVariant != nil {
			converted.PriceVariant = domain.TestPriceVariant(*rule.PriceVariant)
		}
		if rule.Wait != nil {
			converted.Wait = &domain.TestExecutionWait{Kind: rule.Wait.Kind}
			if rule.Wait.Seconds != nil {
				seconds := *rule.Wait.Seconds
				converted.Wait.Seconds = &seconds
			}
		}
		policy.Rules = append(policy.Rules, converted)
	}
	if err := domain.ValidateTestExecutionPolicy(policy); err != nil {
		return domain.TestExecutionPolicy{}, err
	}
	return policy, nil
}

// ToTestExecutionPolicyStateDTO returns a policy snapshot without sharing mutable values.
func ToTestExecutionPolicyStateDTO(connection domain.ExternalID, state domain.TestExecutionPolicyState) TestExecutionPolicyStateDTO {
	out := TestExecutionPolicyStateDTO{Connection: connection.String(), Revision: strconv.FormatUint(state.Revision, 10), TestExecutionPolicyDTO: TestExecutionPolicyDTO{Rules: make([]TestExecutionRuleDTO, 0, len(state.Policy.Rules))}}
	for _, rule := range state.Policy.Rules {
		converted := TestExecutionRuleDTO{Mask: ToInstrumentMaskDTO(rule.Mask), Mode: string(rule.Mode)}
		if rule.Fills != nil {
			fills := *rule.Fills
			converted.Fills = &fills
		}
		if rule.PriceVariant != "" {
			variant := string(rule.PriceVariant)
			converted.PriceVariant = &variant
		}
		if rule.Wait != nil {
			converted.Wait = &TestExecutionWaitDTO{Kind: rule.Wait.Kind}
			if rule.Wait.Seconds != nil {
				seconds := *rule.Wait.Seconds
				converted.Wait.Seconds = &seconds
			}
		}
		out.Rules = append(out.Rules, converted)
	}
	return out
}
