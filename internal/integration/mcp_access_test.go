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
	"errors"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

// TestDisabledCommandRefusesWithNotice verifies that a command the operator has
// disabled in the panel refuses the call: a tool error whose text tells the
// agent the command is off, for every implemented handler.
func TestDisabledCommandRefusesWithNotice(t *testing.T) {
	cases := []struct {
		name string
		call func(src frameworkmcp.Source) (bool, []sdkmcp.Content)
	}{
		{
			name: healthToolName,
			call: func(src frameworkmcp.Source) (bool, []sdkmcp.Content) {
				r := callHealth(t, src)
				return r.IsError, r.Content
			},
		},
		{
			name: getAccountStateToolName,
			call: func(src frameworkmcp.Source) (bool, []sdkmcp.Content) {
				r := callGetAccountState(t, src, "acc-1")
				return r.IsError, r.Content
			},
		},
		{
			name: getLimitsToolName,
			call: func(src frameworkmcp.Source) (bool, []sdkmcp.Content) {
				r := callGetLimits(t, src, "")
				return r.IsError, r.Content
			},
		},
		{
			name: getAuditToolName,
			call: func(src frameworkmcp.Source) (bool, []sdkmcp.Content) {
				r := callGetAudit(t, src, 0)
				return r.IsError, r.Content
			},
		},
		{
			name: checkOrderToolName,
			call: func(src frameworkmcp.Source) (bool, []sdkmcp.Content) {
				r := callCheckOrder(t, src, checkOrderInput{Account: "acc-1"})
				return r.IsError, r.Content
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{disabledCommands: map[string]bool{tc.name: true}}
			isErr, content := tc.call(src)
			requireDisabledNotice(t, isErr, content, tc.name)
		})
	}
}

// TestEnabledCommandRuns verifies that an enabled command runs normally: the
// gate does not short-circuit when the source reports the command enabled.
func TestEnabledCommandRuns(t *testing.T) {
	src := &fakeSource{
		status: frameworkmcp.Status{Healthy: true, Node: frameworkmcp.NodeHealth{
			Engine: frameworkmcp.EngineHealth{Version: "v1", Running: true},
			Store:  frameworkmcp.StoreHealth{Reachable: true},
		}},
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
		status: frameworkmcp.Status{Healthy: true, Node: frameworkmcp.NodeHealth{
			Engine: frameworkmcp.EngineHealth{Version: "v1", Running: true},
			Store:  frameworkmcp.StoreHealth{Reachable: true},
		}},
	}
	res := callHealth(t, src)
	requireNotToolError(t, res.IsError)
	if got := textContent(res.Content); !strings.Contains(got, "officer healthy") {
		t.Fatalf("expected handler to run despite access error, got %q", got)
	}
}

// TestMutatingCommandEnabledErrorFailsClosed verifies that a transient
// access-store error blocks a mutating command (fail-closed): the gate must not
// allow the mutation when it cannot determine whether the command is enabled.
func TestMutatingCommandEnabledErrorFailsClosed(t *testing.T) {
	src := &fakeSource{
		cmdEnabledErr: errors.New("access store down"),
	}
	res := callSetMarketDataInstrument(t, src, setMarketDataInstrumentInput{
		InstanceExternalID: "mock-1", ExternalSymbol: "AAPL", Enabled: true,
	})
	requireToolError(t, res.IsError)
	if len(src.setMDCalls) != 0 {
		t.Fatalf("mutating command must not execute when gate check fails: %+v", src.setMDCalls)
	}
}
