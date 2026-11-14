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

package mcptools

import (
	"context"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/framework/web/httpapi/dto"
)

const getTestExecutionPolicyDescription = "Read the execution policy and revision of a test trading connection. Read-only - credentials are never exposed."
const setTestExecutionPolicyDescription = "Replace the complete execution policy of one test trading connection. Rules use current quotes, not historical replay. Exactly one rule must match each order; no match (including an empty policy) or overlap refuses the order. Venue connections are refused. Mutating, protected and disabled by default."

type getTestExecutionPolicyInput struct {
	Connection string `json:"connection" jsonschema:"Required. Trading connection id; only a test-class connection is accepted."`
}

type setTestExecutionPolicyInput struct {
	Connection string                     `json:"connection" jsonschema:"Required. Existing test trading connection id whose entire policy is replaced; venue connections are refused."`
	Policy     dto.TestExecutionPolicyDTO `json:"policy" jsonschema:"Required. Complete replacement policy for this connection, not a partial update. An empty rules array refuses every order."`
}

func setTestExecutionPolicyHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[setTestExecutionPolicyInput, dto.TestExecutionPolicyStateDTO] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in setTestExecutionPolicyInput) (string, dto.TestExecutionPolicyStateDTO, error) {
		policy, err := dto.FromTestExecutionPolicyDTO(in.Policy)
		if err != nil {
			return "", dto.TestExecutionPolicyStateDTO{}, testPolicyToolError(err)
		}
		state, err := src.ReplaceTestExecutionPolicy(ctx, in.Connection, policy)
		if err != nil {
			return "", dto.TestExecutionPolicyStateDTO{}, testPolicyToolError(err)
		}
		return "Policy replaced", dto.ToTestExecutionPolicyStateDTO(domain.ExternalID(strings.TrimSpace(in.Connection)), state), nil
	}
}

func getTestExecutionPolicyHandler(src frameworkmcp.Source) frameworkmcp.ToolBody[getTestExecutionPolicyInput, dto.TestExecutionPolicyStateDTO] {
	return func(ctx context.Context, _ *sdkmcp.ServerSession, in getTestExecutionPolicyInput) (string, dto.TestExecutionPolicyStateDTO, error) {
		state, err := src.GetTestExecutionPolicy(ctx, in.Connection)
		if err != nil {
			return "", dto.TestExecutionPolicyStateDTO{}, testPolicyToolError(err)
		}
		return "Test execution policy", dto.ToTestExecutionPolicyStateDTO(domain.ExternalID(strings.TrimSpace(in.Connection)), state), nil
	}
}

func testPolicyToolError(err error) error {
	_, body := httpx.DescribeErrorBody(err)
	message := httpx.DescribeErr(err).Message
	if problem, ok := body.(httpx.ProblemDetails); ok {
		message = problem.Detail
	}
	return &frameworkmcp.PublicToolError{Body: body, Message: message}
}

func testExecutionPolicySchema() (*jsonschema.Schema, error) {
	policy, err := jsonschema.For[dto.TestExecutionPolicyDTO](nil)
	if err != nil {
		return nil, err
	}
	rules := policy.Properties["rules"]
	rules.Description = "Ordered execution rules for this connection. The first matching symbol mask wins, from top to bottom. An unknown symbol yields test_policy_no_match. An empty array refuses every order."
	rules.Type, rules.Types = "array", nil
	rule := rules.Items
	rule.Properties["mask"].Description = "Match both base and quote asset masks; use '*' for both to match every instrument."
	rule.Properties["mode"].Description = "execute fills once; split fills in two or three positive parts; reject_even refuses every second matching new order, sharing a counter across this connection and revision; rest waits for a finite delay or remains open without automatic execution."
	rule.Properties["mode"].Enum = []any{"execute", "split", "reject_even", "rest"}
	variant := rule.Properties["priceVariant"]
	variant.Description = "Market orders: normal and worst buy at ask and sell at bid; best buys at bid and sells at ask. Limit orders always use their own limit price. Infinite rest forbids this member."
	variant.Type, variant.Types = "string", nil
	variant.Enum = []any{"normal", "best", "worst"}
	fills := rule.Properties["fills"]
	fills.Description = "Required only for split: two or three positive fills at the submitted quantity's decimal precision. Unrepresentable splits are refused with test_split_unrepresentable."
	fills.Type, fills.Types = "integer", nil
	fills.Enum = []any{2, 3}
	wait := rule.Properties["wait"]
	wait.Description = "Required only for rest. Finite rest executes after seconds using execution-time quotes; infinite rest never executes automatically. There is no individual cancel, and any runtime restart loses the book."
	wait.Type, wait.Types = "object", nil
	wait.Properties["kind"].Enum = []any{"finite", "infinite"}
	seconds := wait.Properties["seconds"]
	seconds.Type, seconds.Types = "integer", nil
	seconds.Minimum, seconds.Maximum = jsonschema.Ptr(1.0), jsonschema.Ptr(9223372036.0)
	wait.OneOf = []*jsonschema.Schema{
		{Properties: map[string]*jsonschema.Schema{"kind": {Enum: []any{"finite"}}}, Required: []string{"seconds"}},
		{Properties: map[string]*jsonschema.Schema{"kind": {Enum: []any{"infinite"}}, "seconds": {Not: &jsonschema.Schema{}}}},
	}
	rule.OneOf = []*jsonschema.Schema{
		{Properties: map[string]*jsonschema.Schema{
			"mode": {Enum: []any{"execute", "reject_even"}}, "fills": {Not: &jsonschema.Schema{}}, "wait": {Not: &jsonschema.Schema{}},
		}, Required: []string{"priceVariant"}},
		{Properties: map[string]*jsonschema.Schema{
			"mode": {Enum: []any{"split"}}, "wait": {Not: &jsonschema.Schema{}},
		}, Required: []string{"priceVariant", "fills"}},
		{Properties: map[string]*jsonschema.Schema{
			"mode": {Enum: []any{"rest"}}, "fills": {Not: &jsonschema.Schema{}},
			"wait": {Properties: map[string]*jsonschema.Schema{"kind": {Enum: []any{"finite"}}}},
		}, Required: []string{"priceVariant", "wait"}},
		{Properties: map[string]*jsonschema.Schema{
			"mode": {Enum: []any{"rest"}}, "fills": {Not: &jsonschema.Schema{}}, "priceVariant": {Not: &jsonschema.Schema{}},
			"wait": {Properties: map[string]*jsonschema.Schema{"kind": {Enum: []any{"infinite"}}}},
		}, Required: []string{"wait"}},
	}
	return policy, nil
}

func testExecutionPolicyOutputSchema(state *jsonschema.Schema) *jsonschema.Schema {
	problem, err := jsonschema.For[httpx.ProblemDetails](nil)
	if err != nil {
		panic(err)
	}
	envelope := &jsonschema.Schema{Type: "object", Required: []string{"error"}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, Properties: map[string]*jsonschema.Schema{
		"error": {Type: "object", Required: []string{"code", "message"}, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, Properties: map[string]*jsonschema.Schema{
			"code": {Type: "string"}, "message": {Type: "string"},
		}},
	}}
	state.Properties["rules"].Type = "array"
	state.Properties["rules"].Types = nil
	var zero any = dto.TestExecutionPolicyStateDTO{}
	return &jsonschema.Schema{Type: "object", AnyOf: []*jsonschema.Schema{state, problem, envelope, {Const: &zero}}}
}
