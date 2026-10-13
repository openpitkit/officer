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

package integration_test

import (
	"context"
	"fmt"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

// assetsSource records the asset calls the asset tools make and answers them
// with stored and the recorder's err.
type assetsSource struct {
	fakeSource
	sourceRecorder
	stored domain.Asset
}

func (s *assetsSource) CreateAsset(_ context.Context, asset domain.Asset) (domain.Asset, error) {
	return s.stored, s.record("create", asset)
}

func (s *assetsSource) UpdateAsset(_ context.Context, oldCode string, asset domain.Asset) (domain.Asset, error) {
	return s.stored, s.record("update", oldCode, asset)
}

func (s *assetsSource) DeleteAsset(_ context.Context, code string, force bool) error {
	return s.record("delete", code, force)
}

// assetsFixture answers with a stored asset that differs from every input the
// tests send and carries an engine id, so a result shows the stored row in the
// REST shape, which leaves the engine id out.
func assetsFixture() *assetsSource {
	return &assetsSource{stored: domain.Asset{
		Code: "BTC", Title: "Bitcoin", AssetClass: "crypto", EngineAssetID: 7,
	}}
}

// storedAssetBody is the REST asset body of assetsFixture's stored row.
const storedAssetBody = `{"asset":{"code":"BTC","title":"Bitcoin","assetClass":"crypto"}}`

// assetToolInputs holds one valid input per asset tool.
var assetToolInputs = map[string]map[string]any{
	"create_asset": {"code": "ETH"},
	"update_asset": {"asset": "ETH", "code": "ETH"},
	"delete_asset": {"asset": "ETH"},
}

// assetsRecorded is assetsFixture as the shared tool drivers take it.
func assetsRecorded() (frameworkmcp.Source, *fakeSource, *sourceRecorder) {
	src := assetsFixture()
	return src, &src.fakeSource, &src.sourceRecorder
}

// TestAssetToolsSuccess covers each asset tool's success: its arguments reach
// the Source verbatim, spaces included, and the result is the REST body.
func TestAssetToolsSuccess(t *testing.T) {
	t.Parallel()
	runToolSuccess(t, assetsRecorded, []toolSuccessCase{
		{"create_asset", map[string]any{"code": " ETH ", "title": " Ether ", "assetClass": " crypto "},
			[]string{"create"}, []any{domain.Asset{Code: " ETH ", Title: " Ether ", AssetClass: " crypto "}},
			"asset BTC created", storedAssetBody},
		{"create_asset", map[string]any{"code": "ETH"},
			[]string{"create"}, []any{domain.Asset{Code: "ETH"}},
			"asset BTC created", storedAssetBody},
		{"update_asset", map[string]any{"asset": " ETH ", "code": " BTC ", "title": " Bitcoin ", "assetClass": " coin "},
			[]string{"update"}, []any{" ETH ", domain.Asset{Code: " BTC ", Title: " Bitcoin ", AssetClass: " coin "}},
			"asset BTC updated", storedAssetBody},
		{"update_asset", map[string]any{"asset": "ETH", "code": "ETH"},
			[]string{"update"}, []any{"ETH", domain.Asset{Code: "ETH"}},
			"asset BTC updated", storedAssetBody},
		{"delete_asset", map[string]any{"asset": " ETH ", "force": true},
			[]string{"delete"}, []any{" ETH ", true},
			"asset  ETH  deleted", `{"asset":" ETH ","force":true}`},
		{"delete_asset", map[string]any{"asset": "ETH"},
			[]string{"delete"}, []any{"ETH", false},
			"asset ETH deleted", `{"asset":"ETH"}`},
	})
}

// TestAssetToolsSourceRefusal covers a Source refusal of each asset tool: it
// reaches the agent under the code REST answers with the Source's message, the
// dependents blocking a delete listed in it.
func TestAssetToolsSourceRefusal(t *testing.T) {
	t.Parallel()
	runToolRefusals(t, assetsRecorded, assetToolInputs, []toolRefusal{
		{"validation", fmt.Errorf("asset code: %w", domain.ErrInvalid),
			"validation: asset code: invalid"},
		{"conflict", fmt.Errorf("asset %q: %w", "BTC", domain.ErrAlreadyExists),
			`conflict: asset "BTC": already exists`},
		{"not_found", fmt.Errorf("asset %q: %w", "ETH", domain.ErrNotFound),
			`not_found: asset "ETH": not found`},
		{"has_dependents", fmt.Errorf("asset %q: %w", "ETH", domain.NewHasDependentsError(
			[]domain.DependentCount{{Kind: "account_currency", Count: 1}, {Kind: "market_data_instrument", Count: 2}},
		)), "has_dependents: has dependents; account_currency (1); market_data_instrument (2)"},
	})
}

// TestAssetToolsDisabled covers each asset tool with its command disabled: the
// agent receives the panel notice and the Source is not called.
func TestAssetToolsDisabled(t *testing.T) {
	t.Parallel()
	runToolsDisabled(t, assetsRecorded, assetToolInputs)
}
