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

package schema

import "go.openpit.dev/officer/framework/domain"

const (
	SourceKindTable             = "source_kind"
	OrderEventTypeTable         = "order_event_type"
	OrderSideTable              = "order_side"
	OrderAmountKindTable        = "order_amount_kind"
	OrderStatusTable            = "order_status"
	AdjustmentStatusTable       = "adjustment_status"
	AuditActionTable            = "audit_action"
	AttestationAlgTable         = "attestation_alg"
	AttestationRequestTypeTable = "attestation_request_type"
	AttestationModeTable        = "attestation_mode"
)

// EnumCodeSeed assigns a stable integer id to one canonical enum code.
type EnumCodeSeed struct {
	ID   int64
	Code string
}

// EnumDictionarySeed defines the complete stable code set for one dictionary.
type EnumDictionarySeed struct {
	Table string
	Codes []EnumCodeSeed
}

// EnumDictionarySeeds returns the canonical dictionary seed data. Each call
// returns an independent slice so connectors cannot mutate shared definitions.
func EnumDictionarySeeds() []EnumDictionarySeed {
	return []EnumDictionarySeed{
		{SourceKindTable, []EnumCodeSeed{
			{1, string(domain.SourcePanel)},
			{2, string(domain.SourceAPI)},
			{3, string(domain.SourceMCP)},
			{4, string(domain.SourceSystem)},
		}},
		{OrderEventTypeTable, []EnumCodeSeed{
			{1, string(domain.OrderEventSubmitted)},
			{2, string(domain.OrderEventPreTradeAccepted)},
			{3, string(domain.OrderEventPreTradeRejected)},
			{4, string(domain.OrderEventCommitted)},
			{5, string(domain.OrderEventRolledBack)},
			{6, string(domain.OrderEventConfirmed)},
			{7, string(domain.OrderEventFill)},
			{8, string(domain.OrderEventCancelled)},
		}},
		{OrderSideTable, []EnumCodeSeed{
			{1, string(domain.OrderSideBuy)},
			{2, string(domain.OrderSideSell)},
		}},
		{OrderAmountKindTable, []EnumCodeSeed{
			{1, string(domain.OrderAmountKindQuantity)},
			{2, string(domain.OrderAmountKindVolume)},
		}},
		{OrderStatusTable, []EnumCodeSeed{
			{1, string(domain.OrderStatusSubmitted)},
			{2, string(domain.OrderStatusAccepted)},
			{3, string(domain.OrderStatusRejected)},
			{4, string(domain.OrderStatusCommitted)},
			{5, string(domain.OrderStatusRolledBack)},
			{6, string(domain.OrderStatusFilled)},
			{7, string(domain.OrderStatusPartiallyFilled)},
			{8, string(domain.OrderStatusCancelled)},
		}},
		{AdjustmentStatusTable, []EnumCodeSeed{
			{1, string(domain.AdjustmentStatusAccepted)},
			{2, string(domain.AdjustmentStatusRejected)},
		}},
		{AuditActionTable, []EnumCodeSeed{
			{1, string(domain.AuditActionHydrate)},
			{2, string(domain.AuditActionCreateAsset)},
			{3, string(domain.AuditActionUpdateAsset)},
			{4, string(domain.AuditActionDeleteAsset)},
			{5, string(domain.AuditActionCreateAccount)},
			{6, string(domain.AuditActionUpdateAccount)},
			{7, string(domain.AuditActionDeleteAccount)},
			{8, string(domain.AuditActionBlock)},
			{9, string(domain.AuditActionUnblock)},
			{10, string(domain.AuditActionSetLimit)},
			{11, string(domain.AuditActionDeleteLimit)},
			{12, string(domain.AuditActionSetGroupNotes)},
			{13, string(domain.AuditActionBlockGroup)},
			{14, string(domain.AuditActionUnblockGroup)},
			{15, string(domain.AuditActionSetNotes)},
			{16, string(domain.AuditActionSetGroup)},
			{17, string(domain.AuditActionSetAccountCurrency)},
			{18, string(domain.AuditActionSetGroupCurrency)},
			{19, string(domain.AuditActionAdjustment)},
			{20, string(domain.AuditActionCreateGroup)},
			{21, string(domain.AuditActionUpdateGroup)},
			{22, string(domain.AuditActionDeleteGroup)},
			{23, string(domain.AuditActionCreateAssetClass)},
			{24, string(domain.AuditActionUpdateAssetClass)},
			{25, string(domain.AuditActionDeleteAssetClass)},
			{26, string(domain.AuditActionSubmitOrder)},
			{27, string(domain.AuditActionExecutionReport)},
			{28, string(domain.AuditActionSetMcpAccess)},
			{29, string(domain.AuditActionSetMarketData)},
			{30, string(domain.AuditActionExportBackup)},
			{31, string(domain.AuditActionRestoreBackup)},
			{32, string(domain.AuditActionExportBusinessCSV)},
			{33, string(domain.AuditActionImportBusinessCSV)},
			{34, string(domain.AuditActionResetDatabase)},
			{35, string(domain.AuditActionRestartService)},
			{36, string(domain.AuditActionStopService)},
			{37, string(domain.AuditActionGenerateSigningKey)},
			{38, string(domain.AuditActionImportSigningKey)},
			{39, string(domain.AuditActionSetSigningConfig)},
			{40, string(domain.AuditActionApprovalIssued)},
			{41, string(domain.AuditActionApprovalFailed)},
			{42, string(domain.AuditActionApprovalConfirmed)},
			{43, string(domain.AuditActionApprovalCancelled)},
		}},
		{AttestationAlgTable, []EnumCodeSeed{
			{1, "ed25519"},
			{2, "none"},
		}},
		{AttestationRequestTypeTable, []EnumCodeSeed{
			{1, string(domain.AttestationRequestSubmit)},
			{2, string(domain.AttestationRequestExecutionReport)},
			{3, string(domain.AttestationRequestConfirm)},
			{4, string(domain.AttestationRequestCancel)},
		}},
		{AttestationModeTable, []EnumCodeSeed{
			{1, "immediate"},
			{2, "hold"},
		}},
	}
}
