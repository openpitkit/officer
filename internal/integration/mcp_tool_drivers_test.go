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
	"fmt"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

// recordedFixture builds a fresh fake Source together with the fakeSource and
// the sourceRecorder it embeds.
type recordedFixture func() (frameworkmcp.Source, *fakeSource, *sourceRecorder)

// toolSuccessCase is one tool call that succeeds: the Source operations it
// makes with the arguments of the last one, its text and its result body.
type toolSuccessCase struct {
	name  string
	input map[string]any
	calls []string
	args  []any
	text  string
	want  string
}

// toolRefusal is a Source error and the tool error text it reaches the agent
// as.
type toolRefusal struct {
	name string
	err  error
	want string
}

// accountsNotManagedRefusal is the core's single-account refusal of a change
// to the account set, accounts and groups alike, and the text it reaches the
// agent as.
var accountsNotManagedRefusal = toolRefusal{"accounts_not_managed", fmt.Errorf(
	"the account set cannot be changed in single-account mode: %w", domain.ErrAccountsNotManaged,
), "accounts_not_managed: the account set cannot be changed in single-account mode: " +
	"accounts and groups are not managed by clients"}

// runToolSuccess covers each case's success: its arguments reach the Source
// verbatim and the result is the expected text and body.
func runToolSuccess(t *testing.T, fixture recordedFixture, cases []toolSuccessCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src, _, recorder := fixture()
			res := callTool[map[string]any](t, src, tc.name, tc.input)
			requireNotToolError(t, res.IsError)
			if !reflect.DeepEqual(recorder.calls, tc.calls) || !reflect.DeepEqual(recorder.arguments, tc.args) {
				t.Fatalf("delegation mismatch: calls %v args %#v", recorder.calls, recorder.arguments)
			}
			assertTradingWire(t, res.Wire, tc.want)
			if got := textContent(res.Content); got != tc.text {
				t.Fatalf("text = %q, want %q", got, tc.text)
			}
		})
	}
}

// runToolRefusals covers each refusal of each tool in inputs: the Source error
// reaches the agent as the refusal's text after the one Source call.
func runToolRefusals(
	t *testing.T, fixture recordedFixture, inputs map[string]map[string]any, refusals []toolRefusal,
) {
	t.Helper()
	for name, input := range inputs {
		for _, refusal := range refusals {
			t.Run(name+"/"+refusal.name, func(t *testing.T) {
				src, _, recorder := fixture()
				recorder.err = refusal.err
				res := callTool[map[string]any](t, src, name, input)
				requireToolError(t, res.IsError)
				if got := textContent(res.Content); got != refusal.want {
					t.Fatalf("refusal = %q, want %q", got, refusal.want)
				}
				if len(recorder.calls) != 1 {
					t.Fatalf("calls = %v, want the one Source call that refused", recorder.calls)
				}
			})
		}
	}
}

// runToolsDisabled covers each tool in inputs with its command disabled: the
// agent receives the panel notice and the Source is not called.
func runToolsDisabled(t *testing.T, fixture recordedFixture, inputs map[string]map[string]any) {
	t.Helper()
	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			src, fake, recorder := fixture()
			fake.disabledCommands = map[string]bool{name: true}
			res := callTool[map[string]any](t, src, name, input)
			requireNotToolError(t, res.IsError)
			text := textContent(res.Content)
			if !strings.Contains(text, fmt.Sprintf("Command %q is disabled in the Pit Officer panel", name)) {
				t.Fatalf("expected the disabled notice, got %q", text)
			}
			if len(recorder.calls) != 0 {
				t.Fatalf("disabled tool reached Source: %v", recorder.calls)
			}
		})
	}
}
