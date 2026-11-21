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

package httpapi

import "go.openpit.dev/officer/framework/domain"

// OrderRejectDTO is the rejection wire shape shared by REST and MCP.
type OrderRejectDTO struct {
	Code    string `json:"code"`
	Scope   string `json:"scope"`
	Policy  string `json:"policy"`
	Reason  string `json:"reason"`
	Details string `json:"details"`
}

// ToOrderRejectDTO maps one rejection without interpreting its fields.
func ToOrderRejectDTO(rej domain.OrderReject) OrderRejectDTO {
	return OrderRejectDTO{Code: rej.Code, Scope: rej.Scope, Policy: rej.Policy, Reason: rej.Reason, Details: rej.Details}
}

// ToOrderRejectDTOs preserves rejection order and returns nil for no
// rejections.
func ToOrderRejectDTOs(rejects []domain.OrderReject) []OrderRejectDTO {
	if len(rejects) == 0 {
		return nil
	}
	out := make([]OrderRejectDTO, 0, len(rejects))
	for _, rej := range rejects {
		out = append(out, ToOrderRejectDTO(rej))
	}
	return out
}
