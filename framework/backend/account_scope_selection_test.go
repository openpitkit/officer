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

package backend

import (
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestRestrictedAccountRejectsSelection(t *testing.T) {
	for _, selection := range []*domain.AccountSelection{{}, {Accounts: []domain.EngineAccountID{7}}} {
		for _, account := range []domain.AccountID{"", "a"} {
			got, ok, err := restrictedAccount(account, domain.AccountRestriction{Account: "a", Selection: selection})
			if !errors.Is(err, domain.ErrForbidden) || ok || got != "" {
				t.Errorf("restrictedAccount(%q, %+v) = %q, %t, %v; want forbidden", account, selection, got, ok, err)
			}
		}
	}
}
