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

// AssetDTO is the wire shape of a single asset dictionary record. Its public
// code is operator-chosen and renameable; the store surrogate id is never
// serialized.
type AssetDTO struct {
	Code       string `json:"code"`
	Title      string `json:"title"`
	AssetClass string `json:"assetClass"`
}

// ToAssetDTO maps a domain.Asset onto the wire DTO.
func ToAssetDTO(asset domain.Asset) AssetDTO {
	return AssetDTO{
		Code:       asset.Code,
		Title:      asset.Title,
		AssetClass: asset.AssetClass,
	}
}
