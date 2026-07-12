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

package sqlite

import fwstore "go.openpit.dev/officer/framework/store"

type Store = fwstore.Store
type RealmStore = fwstore.RealmStore
type AuditEntry = fwstore.AuditEntry
type BusinessCSVImport = fwstore.BusinessCSVImport
type BusinessCSVImportGroup = fwstore.BusinessCSVImportGroup
type BusinessCSVImportAccount = fwstore.BusinessCSVImportAccount
type TextMatcher = fwstore.TextMatcher
type SortSpec = fwstore.SortSpec
type PageSpec = fwstore.PageSpec
type AccountListFilter = fwstore.AccountListFilter
type AccountListRow = fwstore.AccountListRow
type GroupListFilter = fwstore.GroupListFilter
type GroupListRow = fwstore.GroupListRow
type AssetListFilter = fwstore.AssetListFilter
type AssetClassListFilter = fwstore.AssetClassListFilter
type AssetClassListRow = fwstore.AssetClassListRow
type StatusFilter = fwstore.StatusFilter
type CountFilter = fwstore.CountFilter
type CountRangeFilter = fwstore.CountRangeFilter
type DecimalRangeFilter = fwstore.DecimalRangeFilter
type PolicyKind = fwstore.PolicyKind
type PolicyListFilter = fwstore.PolicyListFilter
type PolicyListRow = fwstore.PolicyListRow

const (
	StatusFilterActive           = fwstore.StatusFilterActive
	StatusFilterBlocked          = fwstore.StatusFilterBlocked
	CountFilterHas               = fwstore.CountFilterHas
	CountFilterNone              = fwstore.CountFilterNone
	PolicyKindRate               = fwstore.PolicyKindRate
	PolicyKindOrderSize          = fwstore.PolicyKindOrderSize
	PolicyKindSpotFundsPnlBounds = fwstore.PolicyKindSpotFundsPnlBounds
)

func ExactTextMatcher(value string) TextMatcher { return fwstore.ExactTextMatcher(value) }
