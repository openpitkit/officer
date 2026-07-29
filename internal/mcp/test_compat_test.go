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
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/internal/mcp/tools"
)

const auditDefaultLimit = 50
const auditMaxLimit = 500
const healthToolName = "health"
const getAccountStateToolName = "get_account_state"
const getLimitsToolName = "get_limits"
const getAuditToolName = "get_audit"
const checkOrderToolName = "check_order"
const setMarketDataInstrumentToolName = "set_market_data_instrument"
const submitOrderToolName = "submit_order"
const confirmExecutionToolName = "confirm_execution"
const cancelToolName = "cancel"

type healthInput = tools.HealthInput
type healthOutput = tools.HealthOutput
type getAccountStateInput = tools.GetAccountStateInput
type getAccountStateOutput = tools.GetAccountStateOutput
type getLimitsInput = tools.GetLimitsInput
type getLimitsOutput = tools.GetLimitsOutput
type getOrderInput = tools.GetOrderInput
type getOrderOutput = tools.GetOrderOutput
type getAuditInput = tools.GetAuditInput
type getAuditOutput = tools.GetAuditOutput
type checkOrderInput = tools.CheckOrderInput
type checkOrderOutput = tools.CheckOrderOutput
type setMarketDataInstrumentInput = tools.SetMarketDataInstrumentInput
type setMarketDataInstrumentOutput = tools.SetMarketDataInstrumentOutput
type submitOrderInput = tools.SubmitOrderInput
type submitOrderOutput = tools.SubmitOrderOutput
type submitDropCopyOrderInput = tools.SubmitDropCopyOrderInput
type submitDropCopyOrderOutput = tools.SubmitDropCopyOrderOutput
type confirmExecutionInput = tools.ConfirmExecutionInput
type confirmExecutionOutput = tools.ConfirmExecutionOutput
type cancelInput = tools.CancelInput
type cancelOutput = tools.CancelOutput

func healthHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[healthInput],
) (*sdkmcp.CallToolResultFor[healthOutput], error) {
	return testGuard("health", src, tools.HealthHandler(src))
}

func getAccountStateHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getAccountStateInput],
) (*sdkmcp.CallToolResultFor[getAccountStateOutput], error) {
	return testGuard("get_account_state", src, tools.GetAccountStateHandler(src))
}

func getLimitsHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getLimitsInput],
) (*sdkmcp.CallToolResultFor[getLimitsOutput], error) {
	return testGuard("get_limits", src, tools.GetLimitsHandler(src))
}

func getOrderHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getOrderInput],
) (*sdkmcp.CallToolResultFor[getOrderOutput], error) {
	return testGuard("get_order", src, tools.GetOrderHandler(src))
}

func getAuditHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[getAuditInput],
) (*sdkmcp.CallToolResultFor[getAuditOutput], error) {
	return testGuard("get_audit", src, tools.GetAuditHandler(src))
}

func checkOrderHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[checkOrderInput],
) (*sdkmcp.CallToolResultFor[checkOrderOutput], error) {
	return testGuard("check_order", src, tools.CheckOrderHandler(src))
}

func setMarketDataInstrumentHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[setMarketDataInstrumentInput],
) (*sdkmcp.CallToolResultFor[setMarketDataInstrumentOutput], error) {
	return testGuard(
		"set_market_data_instrument",
		src,
		tools.SetMarketDataInstrumentHandler(src),
	)
}

func submitOrderHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[submitOrderInput],
) (*sdkmcp.CallToolResultFor[submitOrderOutput], error) {
	return testGuard("submit_order", src, tools.SubmitOrderHandler(src))
}

func submitDropCopyOrderHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[submitDropCopyOrderInput],
) (*sdkmcp.CallToolResultFor[submitDropCopyOrderOutput], error) {
	return testGuard(
		"submit_drop_copy_order",
		src,
		tools.SubmitDropCopyOrderHandler(src),
	)
}

func confirmExecutionHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[confirmExecutionInput],
) (*sdkmcp.CallToolResultFor[confirmExecutionOutput], error) {
	return testGuard("confirm_execution", src, tools.ConfirmExecutionHandler(src))
}

func cancelHandler(src Source) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[cancelInput],
) (*sdkmcp.CallToolResultFor[cancelOutput], error) {
	return testGuard("cancel", src, tools.CancelHandler(src))
}

func testGuard[In, Out any](
	name string,
	src Source,
	inner frameworkmcp.ToolBody[In, Out],
) func(
	context.Context,
	*sdkmcp.ServerSession,
	*sdkmcp.CallToolParamsFor[In],
) (*sdkmcp.CallToolResultFor[Out], error) {
	return frameworkmcp.Guard(
		testDescriptor(name),
		frameworkmcp.RegisterDeps{Source: src, Authorizer: httpx.AllowAll{}},
		inner,
	)
}

func testDescriptor(name string) frameworkmcp.ToolDescriptor {
	for _, d := range NewToolRegistry(nil).Descriptors() {
		if d.Name == name {
			return d
		}
	}
	return frameworkmcp.ToolDescriptor{Name: name}
}
