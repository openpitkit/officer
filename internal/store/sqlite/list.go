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

package sqlite

import (
	"strings"

	"go.openpit.dev/officer/framework/domain"
)

func whereFromClauses(clauses []string) string {
	if len(clauses) == 0 {
		return ""
	}
	return "\nWHERE " + strings.Join(clauses, " AND ")
}

// appendAccountRestriction appends the clause of a list's mandatory account
// restriction: rows whose account, read from column, is the restricted one,
// plus, with IncludeUnowned, the rows that unowned selects, those that belong
// to no account. unowned is empty for a table whose every row has an account,
// where IncludeUnowned has no rows to add. The zero restriction appends
// nothing.
func appendAccountRestriction(
	clauses *[]string, args *[]any, column, unowned string,
	restriction domain.AccountRestriction,
) {
	if !restriction.Restricted() {
		return
	}
	var clause string
	if restriction.Selection != nil {
		clause = column + " IN (" + selectedAccountQuery(args, restriction, "code") + ")"
	} else {
		clause = column + " = " + selectionBind(args, restriction.Account.String())
	}
	if restriction.IncludeUnowned && unowned != "" {
		clause = "(" + clause + " OR " + unowned + ")"
	}
	*clauses = append(*clauses, clause)
}

func selectionBind(args *[]any, value any) string {
	*args = append(*args, value)
	return "?"
}

// field is a trusted account column, never request input.
func selectedAccountQuery(args *[]any, restriction domain.AccountRestriction, field string) string {
	var terms []string
	if restriction.Selection != nil {
		var accounts, groups []string
		for _, id := range restriction.Selection.Accounts {
			accounts = append(accounts, selectionBind(args, id.Uint64()))
		}
		for _, id := range restriction.Selection.AccountGroups {
			groups = append(groups, selectionBind(args, int64(id)))
		}
		if len(accounts) > 0 {
			terms = append(terms, "sa.id IN ("+strings.Join(accounts, ",")+")")
		}
		if len(groups) > 0 {
			terms = append(terms, "sa.group_id IN ("+strings.Join(groups, ",")+")")
		}
	}
	selection := "0=1"
	if len(terms) > 0 {
		selection = "(" + strings.Join(terms, " OR ") + ")"
	}
	if restriction.Account != "" {
		selection += " AND sa.code = " + selectionBind(args, restriction.Account.String())
	}
	return "SELECT sa." + field + " FROM account sa WHERE " + selection
}

func selectedGroupQuery(args *[]any, restriction domain.AccountRestriction, field string) string {
	var terms []string
	if restriction.Selection != nil && restriction.Account == "" {
		var ids []string
		for _, id := range restriction.Selection.AccountGroups {
			ids = append(ids, selectionBind(args, int64(id)))
		}
		if len(ids) > 0 {
			terms = append(terms, "sg.id IN ("+strings.Join(ids, ",")+")")
		}
	}
	terms = append(terms, "sg.id IN ("+selectedAccountQuery(args, restriction, "group_id")+")")
	return "SELECT sg." + field + " FROM account_group sg WHERE (" + strings.Join(terms, " OR ") + ")"
}

func appendGroupRestriction(clauses *[]string, args *[]any, groupID string, restriction domain.AccountRestriction) {
	appendAccountRestriction(clauses, args, "a.code", "", restriction)
	if restriction.Selection == nil || restriction.Account != "" || len(restriction.Selection.AccountGroups) == 0 {
		return
	}
	var ids []string
	for _, id := range restriction.Selection.AccountGroups {
		ids = append(ids, selectionBind(args, int64(id)))
	}
	last := len(*clauses) - 1
	(*clauses)[last] = "(" + (*clauses)[last] + " OR " + groupID + " IN (" + strings.Join(ids, ",") + "))"
}

func appendPolicySelection(clauses *[]string, args *[]any, restriction domain.AccountRestriction) {
	clause := "account_code IN (" + selectedAccountQuery(args, restriction, "code") + ") OR account_group_code IN (" + selectedGroupQuery(args, restriction, "code") + ")"
	if restriction.IncludeUnowned {
		clause += " OR (account_code IS NULL AND account_group_code IS NULL)"
	}
	*clauses = append(*clauses, "("+clause+")")
}

func appendAuditSelection(clauses *[]string, args *[]any, restriction domain.AccountRestriction) {
	clause := "au.account_identity IN (" + selectedAccountQuery(args, restriction, "id") + ")"
	clause += " OR (au.account_identity = 0 AND au.account_code = '' AND au.group_identity IN (" + selectedGroupQuery(args, restriction, "id") + "))"
	if restriction.IncludeUnowned {
		clause += " OR (au.account_identity = 0 AND au.group_identity = 0 AND au.account_code = '' AND au.group_code = '')"
	}
	*clauses = append(*clauses, "("+clause+")")
}
