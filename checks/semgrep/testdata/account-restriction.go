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

package testdata

import (
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
)

func restrictionGuard(scope domain.AccountRestriction, ptr *domain.AccountRestriction, filter store.OrderListFilter, account domain.Account) {
	// ruleid: account-restriction-must-use-restricted
	_ = scope.Account == ""
	// ruleid: account-restriction-must-use-restricted
	_ = scope.Account != ""
	// ruleid: account-restriction-must-use-restricted
	_ = "" == scope.Account
	// ruleid: account-restriction-must-use-restricted
	_ = "" != scope.Account
	// ruleid: account-restriction-must-use-restricted
	_ = ptr.Account == ""
	// ruleid: account-restriction-must-use-restricted
	_ = filter.Restriction.Account != ""
	// ruleid: account-restriction-must-use-restricted
	_ = filter.Restriction.Account == ""
	// ok: account-restriction-must-use-restricted
	_ = scope.Restricted()
	// ok: account-restriction-must-use-restricted
	_ = account.Code == ""
}

type AccountRestriction struct {
	Account   domain.AccountID
	Selection *domain.AccountSelection
}

func (r AccountRestriction) Restricted() bool {
	// ok: account-restriction-must-use-restricted
	return r.Account != "" || r.Selection != nil
}

func localRestriction(r AccountRestriction) bool {
	// ruleid: account-restriction-must-use-restricted
	return r.Account != ""
}

type order struct {
	Account domain.AccountID
}

func unrelated(o order) bool {
	// ok: account-restriction-must-use-restricted
	return o.Account != ""
}
