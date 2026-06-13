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

package mcp

import (
	"errors"
	"strings"
	"testing"
)

// TestDisabledCommandReturnsNonErrorNotice verifies that a command the operator
// has disabled in the panel still returns a successful (non-error) result whose
// text tells the agent the command is off, for every implemented handler.
func TestDisabledCommandReturnsNonErrorNotice(t *testing.T) {
	cases := []struct {
		name string
		call func(src Source) (bool, []contentText)
	}{
		{
			name: healthToolName,
			call: func(src Source) (bool, []contentText) {
				r := callHealth(t, src)
				return r.IsError, wrap(textContent(r.Content))
			},
		},
		{
			name: getAccountStateToolName,
			call: func(src Source) (bool, []contentText) {
				r := callGetAccountState(t, src, "acc-1")
				return r.IsError, wrap(textContent(r.Content))
			},
		},
		{
			name: getLimitsToolName,
			call: func(src Source) (bool, []contentText) {
				r := callGetLimits(t, src, "")
				return r.IsError, wrap(textContent(r.Content))
			},
		},
		{
			name: getAuditToolName,
			call: func(src Source) (bool, []contentText) {
				r := callGetAudit(t, src, 0)
				return r.IsError, wrap(textContent(r.Content))
			},
		},
		{
			name: checkOrderToolName,
			call: func(src Source) (bool, []contentText) {
				r := callCheckOrder(t, src, checkOrderInput{Account: "acc-1"})
				return r.IsError, wrap(textContent(r.Content))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{disabledCommands: map[string]bool{tc.name: true}}
			isErr, content := tc.call(src)
			requireNotToolError(t, isErr)
			text := content[0].text
			if !strings.Contains(text, "disabled in the Pit Officer panel") {
				t.Fatalf("expected disabled notice, got %q", text)
			}
			if !strings.Contains(text, tc.name) {
				t.Fatalf("disabled notice should name the command %q, got %q", tc.name, text)
			}
		})
	}
}

// TestEnabledCommandRuns verifies that an enabled command runs normally: the
// gate does not short-circuit when the source reports the command enabled.
func TestEnabledCommandRuns(t *testing.T) {
	src := &fakeSource{
		status: Status{Healthy: true, Nodes: []NodeHealth{{
			Engine: EngineHealth{Version: "v1", Running: true},
			Store:  StoreHealth{Reachable: true},
		}}},
	}
	res := callHealth(t, src)
	requireNotToolError(t, res.IsError)
	if got := textContent(res.Content); !strings.Contains(got, "officer healthy") {
		t.Fatalf("expected health summary, got %q", got)
	}
}

// TestCommandEnabledErrorFailsOpen verifies that a transient access-store error
// does not block a read: the gate fails open and the handler runs.
func TestCommandEnabledErrorFailsOpen(t *testing.T) {
	src := &fakeSource{
		cmdEnabledErr: errors.New("access store down"),
		status: Status{Healthy: true, Nodes: []NodeHealth{{
			Engine: EngineHealth{Version: "v1", Running: true},
			Store:  StoreHealth{Reachable: true},
		}}},
	}
	res := callHealth(t, src)
	requireNotToolError(t, res.IsError)
	if got := textContent(res.Content); !strings.Contains(got, "officer healthy") {
		t.Fatalf("expected handler to run despite access error, got %q", got)
	}
}

// contentText is a tiny holder so the table cases can return the extracted text
// uniformly across differently-typed handler results.
type contentText struct{ text string }

func wrap(text string) []contentText { return []contentText{{text: text}} }
