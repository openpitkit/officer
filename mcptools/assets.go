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

package mcptools

import (
	"context"
	"fmt"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

// The asset tools map their JSON arguments 1:1 onto the asset calls REST makes
// and leave every value rule to the core.

const (
	createAssetToolName = "create_asset"
	updateAssetToolName = "update_asset"
	deleteAssetToolName = "delete_asset"
)

const (
	createAssetToolDescription = "Create an asset with a code, an optional " +
		"title and the optional code of an existing asset class. Mutates " +
		"Officer control-plane config; protected and disabled by default."
	updateAssetToolDescription = "Change an asset's code, title and asset " +
		"class; the title and the asset class replace the stored ones, and an " +
		"omitted or empty one clears it. A code change is refused with " +
		"loosening_refused while a rate-limit or order-size barrier references " +
		"the asset or a Spot Funds P&L bound is denominated in it. Mutates " +
		"Officer control-plane config; protected and disabled by default."
	deleteAssetToolDescription = "Delete an asset. An asset with dependent " +
		"rows, the market-data and trading instruments that reference it " +
		"included, is refused with has_dependents, and so is an asset an " +
		"account or group holds as its currency. The delete is refused with " +
		"loosening_refused while a rate-limit or order-size barrier references " +
		"the asset or a Spot Funds P&L bound is denominated in it, and always " +
		"with force: force would delete the asset's dependent rows with it - " +
		"balances, orders, trades, adjustments and barriers among them. " +
		"Mutates Officer control-plane config; protected and disabled by default."
)

type createAssetInput struct {
	Code       string `json:"code" jsonschema:"Asset code"`
	Title      string `json:"title,omitempty" jsonschema:"Asset title; omitted leaves the asset untitled"`
	AssetClass string `json:"assetClass,omitempty" jsonschema:"Code of an existing asset class; omitted leaves the asset unclassified"`
}

type updateAssetInput struct {
	Asset      string `json:"asset" jsonschema:"Current asset code"`
	Code       string `json:"code" jsonschema:"New asset code; equal to asset to keep it"`
	Title      string `json:"title,omitempty" jsonschema:"New asset title, replacing the stored one; omitted or empty clears it"`
	AssetClass string `json:"assetClass,omitempty" jsonschema:"Code of an existing asset class, replacing the stored one; omitted or empty clears it"`
}

type deleteAssetInput struct {
	Asset string `json:"asset" jsonschema:"Asset code"`
	Force bool   `json:"force,omitempty" jsonschema:"Refused with loosening_refused whatever the asset carries: an agent may not force a delete, which would also delete the asset's dependent rows - balances, orders, trades, adjustments and barriers among them, and the market-data and trading instruments that reference it. Without it an asset with dependents is refused with has_dependents"`
}

// assetOutput is the REST asset body, {"asset": {...}}.
type assetOutput struct {
	Asset httpx.AssetDTO `json:"asset"`
}

func createAssetHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[createAssetInput, assetOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in createAssetInput,
	) (string, assetOutput, error) {
		asset, err := src.CreateAsset(ctx, domain.Asset{
			Code:       in.Code,
			Title:      in.Title,
			AssetClass: in.AssetClass,
		})
		if err != nil {
			return "", assetOutput{}, toolError(err)
		}
		out := assetOutput{Asset: httpx.ToAssetDTO(asset)}
		return fmt.Sprintf("asset %s created", asset.Code), out, nil
	}
}

func updateAssetHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[updateAssetInput, assetOutput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in updateAssetInput,
	) (string, assetOutput, error) {
		asset, err := src.UpdateAsset(ctx, in.Asset, domain.Asset{
			Code:       in.Code,
			Title:      in.Title,
			AssetClass: in.AssetClass,
		})
		if err != nil {
			return "", assetOutput{}, toolError(err)
		}
		out := assetOutput{Asset: httpx.ToAssetDTO(asset)}
		return fmt.Sprintf("asset %s updated", asset.Code), out, nil
	}
}

func deleteAssetHandler(
	src frameworkmcp.Source,
) frameworkmcp.ToolBody[deleteAssetInput, deleteAssetInput] {
	return func(
		ctx context.Context,
		_ *sdkmcp.ServerSession,
		in deleteAssetInput,
	) (string, deleteAssetInput, error) {
		if err := src.DeleteAsset(ctx, in.Asset, in.Force); err != nil {
			return "", deleteAssetInput{}, toolError(err)
		}
		return fmt.Sprintf("asset %s deleted", in.Asset), in, nil
	}
}
