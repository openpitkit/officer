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

// groupsSource records the account-group calls the group tools make and
// answers them with stored and the recorder's err.
type groupsSource struct {
	fakeSource
	sourceRecorder
	stored domain.AccountGroup
}

func (s *groupsSource) CreateGroup(_ context.Context, group domain.AccountGroup) (domain.AccountGroup, error) {
	return s.stored, s.record("create", group)
}

func (s *groupsSource) UpdateGroup(_ context.Context, oldCode string, group domain.AccountGroup) (domain.AccountGroup, error) {
	return s.stored, s.record("update", oldCode, group)
}

func (s *groupsSource) DeleteGroup(_ context.Context, code string, force bool) error {
	return s.record("delete", code, force)
}

// groupsFixture answers with a stored group that differs from every input the
// tests send, is blocked and carries an engine id, so a result shows the stored
// row in the REST shape, which leaves the engine id out.
func groupsFixture() *groupsSource {
	return &groupsSource{stored: domain.AccountGroup{
		Code: "traders", Title: "Traders", Currency: "EUR", Notes: "night shift",
		BlockReason: "desk halt", EngineGroupID: 7, Blocked: true,
	}}
}

// storedGroupBody is the REST group body of groupsFixture's stored row.
const storedGroupBody = `{"group":{"code":"traders","title":"Traders","currency":"EUR",` +
	`"notes":"night shift","accountCount":0,"positionCount":0,"blockReason":"desk halt",` +
	`"blocked":true}}`

// groupToolInputs holds one valid input per group tool.
var groupToolInputs = map[string]map[string]any{
	"create_group": {"code": "desks"},
	"update_group": {"group": "desks", "code": "traders", "title": ""},
	"delete_group": {"group": "desks"},
}

// groupsRecorded is groupsFixture as the shared tool drivers take it.
func groupsRecorded() (frameworkmcp.Source, *fakeSource, *sourceRecorder) {
	src := groupsFixture()
	return src, &src.fakeSource, &src.sourceRecorder
}

// TestGroupToolsSuccess covers each group tool's success: its arguments reach
// the Source verbatim, spaces included, and the result is the REST body.
func TestGroupToolsSuccess(t *testing.T) {
	t.Parallel()
	runToolSuccess(t, groupsRecorded, []toolSuccessCase{
		{"create_group", map[string]any{"code": " desks ", "title": " Desks ", "currency": " USD ", "notes": " day shift "},
			[]string{"create"}, []any{domain.AccountGroup{Code: " desks ", Title: " Desks ", Currency: " USD ", Notes: " day shift "}},
			"group traders created", storedGroupBody},
		{"create_group", map[string]any{"code": "desks"},
			[]string{"create"}, []any{domain.AccountGroup{Code: "desks"}},
			"group traders created", storedGroupBody},
		{"update_group", map[string]any{"group": " desks ", "code": " traders ", "title": " Traders "},
			[]string{"update"}, []any{" desks ", domain.AccountGroup{Code: " traders ", Title: " Traders "}},
			"group traders updated", storedGroupBody},
		{"update_group", map[string]any{"group": "desks", "code": "desks", "title": ""},
			[]string{"update"}, []any{"desks", domain.AccountGroup{Code: "desks"}},
			"group traders updated", storedGroupBody},
		{"delete_group", map[string]any{"group": " desks ", "force": true},
			[]string{"delete"}, []any{" desks ", true},
			"group  desks  deleted", `{"group":" desks ","force":true}`},
		{"delete_group", map[string]any{"group": "desks"},
			[]string{"delete"}, []any{"desks", false},
			"group desks deleted", `{"group":"desks"}`},
	})
}

// TestGroupToolsSourceRefusal covers a Source refusal of each group tool: it
// reaches the agent under the code REST's shared mapping answers with the
// Source's message, the dependents blocking a delete listed in it.
func TestGroupToolsSourceRefusal(t *testing.T) {
	t.Parallel()
	runToolRefusals(t, groupsRecorded, groupToolInputs, []toolRefusal{
		{"validation", fmt.Errorf("group id %q is reserved: %w", "-", domain.ErrInvalid),
			`validation: group id "-" is reserved: invalid`},
		{"forbidden", fmt.Errorf("engine: delete group %q: %w", "desks", domain.ErrReservedGroup),
			"forbidden: forbidden"},
		accountsNotManagedRefusal,
		{"conflict", fmt.Errorf("group %q: %w", "traders", domain.ErrAlreadyExists),
			`conflict: group "traders": already exists`},
		{"not_found", fmt.Errorf("group %q: %w", "desks", domain.ErrNotFound),
			`not_found: group "desks": not found`},
		{"has_dependents", fmt.Errorf("group %q: %w", "desks", domain.NewHasDependentsError(
			[]domain.DependentCount{{Kind: "limit_spot_funds_pnl_bound", Count: 2}},
		)), "has_dependents: has dependents; limit_spot_funds_pnl_bound (2)"},
		{"currency_change_blocked", domain.NewCurrencyChangeBlockedError(
			domain.ScopeAccountGroup, "desks",
			fmt.Errorf("account %q carries protected state: %w", "desk-1", domain.ErrConflict),
		), `currency_change_blocked: account "desk-1" carries protected state: conflict`},
	})
}

// TestGroupToolsDisabled covers each group tool with its command disabled: the
// agent receives the panel notice and the Source is not called.
func TestGroupToolsDisabled(t *testing.T) {
	t.Parallel()
	runToolsDisabled(t, groupsRecorded, groupToolInputs)
}
