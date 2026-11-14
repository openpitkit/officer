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

import "go.openpit.dev/officer/framework/domain"

// InstrumentMaskDTO is the shared REST and MCP shape with two required masks;
// base and quote codes match separately, without trimming or normalization.
type InstrumentMaskDTO struct {
	BaseAsset  string `json:"baseAsset" jsonschema:"Base asset code mask; * matches any run of characters including none, every other character is literal, the whole code must match and case matters"`
	QuoteAsset string `json:"quoteAsset" jsonschema:"Quote asset code mask; * matches any run of characters including none, every other character is literal, the whole code must match and case matters"`
}

// ToInstrumentMaskDTO preserves both mask texts byte for byte.
func ToInstrumentMaskDTO(m domain.InstrumentMask) InstrumentMaskDTO {
	return InstrumentMaskDTO{BaseAsset: m.BaseAsset(), QuoteAsset: m.QuoteAsset()}
}

// FromInstrumentMaskDTO validates both masks without changing their text.
// Refusals pass through unchanged with ErrInvalid, the /baseAsset or /quoteAsset
// pointer, and the required (empty) or format constraint.
func FromInstrumentMaskDTO(dto InstrumentMaskDTO) (domain.InstrumentMask, error) {
	return domain.ParseInstrumentMask(dto.BaseAsset, dto.QuoteAsset)
}
