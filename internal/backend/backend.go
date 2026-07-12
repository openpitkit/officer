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

// Package backend preserves the open app's historical import path while the
// control-plane implementation lives in the framework module.
package backend

import (
	"go.openpit.dev/officer/app/engine/native"
	fwbackend "go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/mcp/catalog"
	"go.openpit.dev/officer/framework/node"
	fwsigning "go.openpit.dev/officer/framework/signing"
	appmarketdata "go.openpit.dev/officer/internal/marketdata"
)

type Activity = fwbackend.Activity
type ActivityKind = fwbackend.ActivityKind
type ApprovalToken = fwbackend.ApprovalToken
type BusinessCSVExportRequest = fwbackend.BusinessCSVExportRequest
type BusinessCSVImportPreview = fwbackend.BusinessCSVImportPreview
type BusinessCSVImportRequest = fwbackend.BusinessCSVImportRequest
type BusinessCSVImportResult = fwbackend.BusinessCSVImportResult
type Command = fwbackend.Command
type ControlPlane = fwbackend.ControlPlane
type Counts = fwbackend.Counts
type MarketDataInstanceStatus = fwbackend.MarketDataInstanceStatus
type MarketDataInstrumentStatus = fwbackend.MarketDataInstrumentStatus
type MarketDataProvider = fwbackend.MarketDataProvider
type MarketDataRuntime = fwbackend.MarketDataRuntime
type MarketDataStatus = fwbackend.MarketDataStatus
type MarketDataSymbolMatch = fwbackend.MarketDataSymbolMatch
type MarketDataSymbolSearch = fwbackend.MarketDataSymbolSearch
type MarketDataSymbolSearchInput = fwbackend.MarketDataSymbolSearchInput
type MarketDataSymbolVerification = fwbackend.MarketDataSymbolVerification
type McpCommand = fwbackend.McpCommand
type Overview = fwbackend.Overview
type Service = fwbackend.Service
type ServiceDatabase = fwbackend.ServiceDatabase
type ServiceInfo = fwbackend.ServiceInfo
type Status = fwbackend.Status

const ActivityKindAdjustment = fwbackend.ActivityKindAdjustment
const ActivityKindAudit = fwbackend.ActivityKindAudit
const ActivityKindOrder = fwbackend.ActivityKindOrder
const MarketDataFreshnessTTL = fwbackend.MarketDataFreshnessTTL
const SubmitModeHold = fwbackend.SubmitModeHold
const SubmitModeImmediate = fwbackend.SubmitModeImmediate

// New constructs the open app's framework-backed control-plane service.
func New(
	router node.NodeRouter,
	md MarketDataRuntime,
	signer fwsigning.Service,
) *Service {
	return fwbackend.New(
		router,
		md,
		signer,
		fwbackend.WithMarketDataRegistry(appmarketdata.DefaultRegistry()),
		fwbackend.WithMCPCatalog(defaultMCPCatalog()),
		fwbackend.WithLockSettlementEstimator(native.LockSettlementEstimate),
	)
}

func defaultMCPCatalog() catalog.Catalog {
	return catalog.New([]catalog.CatalogCommand{
		{
			Name:             "health",
			Title:            "Health",
			AgentDescription: "Report Pit Officer deployment health (engine version/profile/liveness, store reachability).",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_account_state",
			Title:            "Get account state",
			AgentDescription: "Read an account row and its account-scoped risk barriers.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_limits",
			Title:            "Get limits",
			AgentDescription: "List configured risk barriers, optionally filtered by account.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_order",
			Title:            "Get order",
			AgentDescription: "Read one order by external id with its approval envelope and fills.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "get_audit",
			Title:            "Get audit",
			AgentDescription: "Read recent control-plane audit-log entries.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "check_order",
			Title:            "Check order",
			AgentDescription: "Run a non-mutating pre-trade dry-run for an order (pass/reject + reasons); changes no state.",
			Mutating:         false,
			Protective:       false,
			Implemented:      true,
			DefaultEnabled:   true,
		},
		{
			Name:             "set_limit",
			Title:            "Set limit",
			AgentDescription: "Create or change a risk-limit / policy barrier.",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
		{
			Name:             "set_market_data_instrument",
			Title:            "Set market-data instrument",
			AgentDescription: "Enable or disable one configured market-data instrument.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "submit_order",
			Title:            "Submit order",
			AgentDescription: "Submit an order intent through pre-trade and obtain a signed approval token.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "confirm_execution",
			Title:            "Confirm execution",
			AgentDescription: "Record confirmation history for an untouched workflow order.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "cancel",
			Title:            "Cancel",
			AgentDescription: "Cancel an untouched workflow order through a derived execution report.",
			Mutating:         true,
			Protective:       true,
			Implemented:      true,
			DefaultEnabled:   false,
		},
		{
			Name:             "get_next_token",
			Title:            "Get next token",
			AgentDescription: "Fetch the next approval token for a trade from the registry.",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
		{
			Name:             "report_fill",
			Title:            "Report fill",
			AgentDescription: "Report an execution fill to the engine (post-trade).",
			Mutating:         true,
			Protective:       true,
			Implemented:      false,
			DefaultEnabled:   false,
		},
	})
}
