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

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	frameworkmcp "go.openpit.dev/officer/framework/mcp"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
	"go.openpit.dev/officer/mcptools"
)

// Tool names and audit limits of the Officer MCP tool set, as its wire
// contract states them.
const (
	auditDefaultLimit               = 50
	auditMaxLimit                   = 500
	healthToolName                  = "health"
	getAccountStateToolName         = "get_account_state"
	getLimitsToolName               = "get_limits"
	getOrderToolName                = "get_order"
	getAuditToolName                = "get_audit"
	checkOrderToolName              = "check_order"
	setMarketDataInstrumentToolName = "set_market_data_instrument"
	submitOrderToolName             = "submit_order"
	submitDropCopyOrderToolName     = "submit_drop_copy_order"
	confirmExecutionToolName        = "confirm_execution"
	cancelToolName                  = "cancel"
)

// The types below mirror the Officer MCP tool inputs and outputs by their JSON
// field names. The tests reach the tools only through an MCP session, so they
// encode inputs and decode structured results on the wire, the way an agent
// does. Inputs carry every field the tool schema requires; outputs carry the
// fields the tests read.

type healthInput struct{}

type healthOutput struct {
	Nodes   []healthNode `json:"nodes"`
	Healthy bool         `json:"healthy"`
}

type healthNode struct {
	Engine healthEngine `json:"engine"`
}

type healthEngine struct {
	Version string `json:"version"`
}

type getAccountStateInput struct {
	Account string `json:"account"`
}

type getAccountStateOutput struct {
	Account accountDTO `json:"account"`
	Limits  limitsDTO  `json:"limits"`
}

type accountDTO struct {
	Code               string `json:"code"`
	Title              string `json:"title"`
	BlockReason        string `json:"blockReason"`
	BlockSource        string `json:"blockSource"`
	AccountBlockReason string `json:"accountBlockReason"`
	GroupBlockReason   string `json:"groupBlockReason"`
	Blocked            bool   `json:"blocked"`
	AccountBlocked     bool   `json:"accountBlocked"`
	GroupBlocked       bool   `json:"groupBlocked"`
}

type limitsDTO struct {
	RateLimits               []rateLimitDTO               `json:"rateLimits"`
	OrderSizeLimits          []orderSizeLimitDTO          `json:"orderSizeLimits"`
	SpotFundsPnlBoundsLimits []spotFundsPnlBoundsLimitDTO `json:"spotFundsPnlBoundsLimits"`
}

type rateLimitDTO struct {
	Account   string `json:"account"`
	MaxOrders uint64 `json:"maxOrders"`
}

type orderSizeLimitDTO struct {
	Scope       string `json:"scope"`
	Account     string `json:"account"`
	Asset       string `json:"asset"`
	MaxQuantity string `json:"maxQuantity"`
	MaxNotional string `json:"maxNotional"`
}

type spotFundsPnlBoundsLimitDTO struct {
	Scope      string `json:"scope"`
	Account    string `json:"account"`
	Currency   string `json:"currency"`
	LowerBound string `json:"lowerBound"`
	UpperBound string `json:"upperBound"`
}

type getLimitsInput struct {
	Account string `json:"account,omitempty"`
}

type getLimitsOutput struct {
	Limits limitsDTO `json:"limits"`
}

type getOrderInput struct {
	OrderExternalID string `json:"id"`
}

type getOrderOutput struct {
	Order    orderDTO          `json:"order"`
	Approval *orderApprovalDTO `json:"approval"`
	Trades   []tradeDTO        `json:"trades"`
}

type orderDTO struct {
	ExternalID          string          `json:"id"`
	CommissionSubtotals []commissionDTO `json:"commissionSubtotals"`
	LeavesQuantity      string          `json:"leavesQuantity"`
	Status              string          `json:"status"`
	DisplayPrice        string          `json:"displayPrice"`
}

type commissionDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type orderApprovalDTO struct {
	Token  string `json:"token"`
	Signed bool   `json:"signed"`
}

type tradeDTO struct {
	Price      string         `json:"price"`
	LockPrice  string         `json:"lockPrice"`
	Commission *commissionDTO `json:"commission,omitempty"`
}

type getAuditInput struct {
	Limit int `json:"limit,omitempty"`
}

type getAuditOutput struct {
	Entries []auditDTO `json:"entries"`
}

type auditDTO struct {
	At         time.Time `json:"at"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Account    string    `json:"account"`
	Group      string    `json:"group"`
	ExternalID string    `json:"id"`
}

type checkOrderInput struct {
	Account     string `json:"account"`
	BaseAsset   string `json:"baseAsset"`
	QuoteAsset  string `json:"quoteAsset"`
	Side        string `json:"side"`
	AmountKind  string `json:"amountKind"`
	AmountValue string `json:"amountValue"`
	Price       string `json:"price,omitempty"`
}

type checkOrderOutput struct {
	WouldBlock        *checkOrderBlockDTO   `json:"wouldBlock"`
	Rejects           []checkOrderRejectDTO `json:"rejects"`
	WouldDisplayPrice string                `json:"wouldDisplayPrice"`
	Passed            bool                  `json:"passed"`
}

type checkOrderRejectDTO struct {
	Code string `json:"code"`
}

type checkOrderBlockDTO struct {
	Account string `json:"account"`
}

type setMarketDataInstrumentInput struct {
	InstanceExternalID string `json:"id"`
	ExternalSymbol     string `json:"externalSymbol"`
	Enabled            bool   `json:"enabled"`
}

type setMarketDataInstrumentOutput struct{}

type submitOrderInput struct {
	Account        string `json:"account"`
	BaseAsset      string `json:"baseAsset"`
	QuoteAsset     string `json:"quoteAsset"`
	Side           string `json:"side"`
	AmountKind     string `json:"amountKind"`
	AmountValue    string `json:"amountValue"`
	Price          string `json:"price,omitempty"`
	Mode           string `json:"mode,omitempty"`
	ExternalID     string `json:"id,omitempty"`
	MissingAccount string `json:"missingAccount"`
}

type submitOrderOutput struct {
	Token           string                `json:"token"`
	KeyID           string                `json:"keyId"`
	OrderExternalID string                `json:"id"`
	Verdict         string                `json:"verdict"`
	Reasons         []checkOrderRejectDTO `json:"reasons,omitempty"`
}

type submitDropCopyOrderInput struct {
	Account        string `json:"account"`
	BaseAsset      string `json:"baseAsset"`
	QuoteAsset     string `json:"quoteAsset"`
	Side           string `json:"side"`
	AmountKind     string `json:"amountKind"`
	AmountValue    string `json:"amountValue"`
	Price          string `json:"price,omitempty"`
	ExternalID     string `json:"id,omitempty"`
	MissingAccount string `json:"missingAccount"`
}

type submitDropCopyOrderOutput struct {
	OrderExternalID string `json:"id"`
	Status          string `json:"status"`
}

type confirmExecutionInput struct {
	OrderExternalID string `json:"id"`
	Token           string `json:"token"`
}

type confirmExecutionOutput struct {
	OrderExternalID string `json:"id"`
	Status          string `json:"status"`
}

type cancelInput struct {
	OrderExternalID string `json:"id"`
	Token           string `json:"token"`
	LeavesQuantity  string `json:"leavesQuantity,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type cancelOutput struct {
	OrderExternalID string `json:"id"`
	Status          string `json:"status"`
}

// toolResult is one tool call's outcome as an MCP client observes it:
// StructuredContent is the payload decoded into the test's mirror of the tool
// output, and Wire is the payload's JSON as the client received it.
type toolResult[Out any] struct {
	IsError           bool
	Content           []sdkmcp.Content
	StructuredContent Out
	Wire              json.RawMessage
}

// officerToolRegistry builds the Officer MCP tool registry and catalogue.
func officerToolRegistry() *frameworkmcp.ToolRegistry {
	reg := frameworkmcp.NewToolRegistry()
	mcptools.RegisterTools(reg, nil)
	return reg
}

// callTool calls one Officer MCP tool over an in-memory MCP session with the
// framework server built over the Officer tool registry, and fails the test on
// a protocol-level error.
func callTool[Out any](
	t *testing.T, src frameworkmcp.Source, name string, in any,
) *toolResult[Out] {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := frameworkmcp.Build(
		officerToolRegistry(), src, nil, httpx.AllowAll{}, operatorCaller(),
	)
	if err != nil {
		t.Fatalf("build MCP server: %v", err)
	}
	serverTransport, clientTransport := sdkmcp.NewInMemoryTransports()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Run(ctx, serverTransport)
	}()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test"}, nil)
	session, err := client.Connect(ctx, clientTransport)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	res, callErr := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: in})
	_ = session.Close()
	cancel()
	if err := <-serverErr; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("server run: %v", err)
	}
	if callErr != nil {
		t.Fatalf("%s returned protocol error: %v", name, callErr)
	}

	out := &toolResult[Out]{IsError: res.IsError, Content: res.Content}
	if res.StructuredContent != nil {
		wire, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal %s structured content: %v", name, err)
		}
		out.Wire = wire
		if err := json.Unmarshal(wire, &out.StructuredContent); err != nil {
			t.Fatalf("decode %s structured content: %v", name, err)
		}
	}
	return out
}
