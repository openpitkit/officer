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
	"errors"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

// callSetMarketDataInstrument invokes the set_market_data_instrument handler
// directly with the given input and fails on a protocol-level error.
func callSetMarketDataInstrument(
	t *testing.T, src Source, in setMarketDataInstrumentInput,
) *sdkmcp.CallToolResultFor[setMarketDataInstrumentOutput] {
	t.Helper()
	h := setMarketDataInstrumentHandler(src)
	res, err := h(context.Background(), nil,
		&sdkmcp.CallToolParamsFor[setMarketDataInstrumentInput]{Arguments: in})
	if err != nil {
		t.Fatalf("setMarketDataInstrumentHandler returned protocol error: %v", err)
	}
	return res
}

// -- set_market_data_instrument: required-field validation --

// TestSetMarketDataInstrumentRequiredFields covers the two required-field
// validation branches: a missing id and a missing externalSymbol each
// yield the matching tool error, and neither reaches the source mutation. The
// command is enabled (default fakeSource) so the gate passes and validation
// runs.
func TestSetMarketDataInstrumentRequiredFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		in      setMarketDataInstrumentInput
		wantMsg string
	}{
		{
			name: "missing id",
			in: setMarketDataInstrumentInput{
				InstanceExternalID: "  ", ExternalSymbol: "AAPL", Enabled: true,
			},
			wantMsg: "id is required",
		},
		{
			name: "missing externalSymbol",
			in: setMarketDataInstrumentInput{
				InstanceExternalID: "mock-1", ExternalSymbol: "  ", Enabled: true,
			},
			wantMsg: "externalSymbol is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeSource{}
			res := callSetMarketDataInstrument(t, src, tc.in)
			requireToolError(t, res.IsError)
			if got := textContent(res.Content); got != tc.wantMsg {
				t.Fatalf("unexpected error text: got %q want %q", got, tc.wantMsg)
			}
			if len(src.setMDCalls) != 0 {
				t.Fatalf("invalid input must not mutate: %+v", src.setMDCalls)
			}
		})
	}
}

// -- set_market_data_instrument: source-error path --

// setMDErrSource is a Source whose SetMarketDataInstrumentEnabled fails. It is
// distinct from the fail-closed gate path (a CommandEnabled error): here the
// gate passes and the mutation itself returns an error, exercising the
// handler's source-error branch.
type setMDErrSource struct {
	setMDErr error
}

func (s *setMDErrSource) Status(context.Context) (Status, error) {
	return Status{}, nil
}

func (s *setMDErrSource) GetAccountState(
	context.Context, domain.AccountID,
) (domain.Account, node.AccountLimits, error) {
	return domain.Account{}, node.AccountLimits{}, nil
}

func (s *setMDErrSource) ListGroups(
	context.Context,
) ([]domain.AccountGroup, error) {
	return nil, nil
}

func (s *setMDErrSource) ListLimits(
	context.Context, domain.AccountID,
) (node.AccountLimits, error) {
	return node.AccountLimits{}, nil
}

func (s *setMDErrSource) GetOrder(
	context.Context, string,
) (domain.OrderDetail, error) {
	return domain.OrderDetail{}, nil
}

func (s *setMDErrSource) ListAudit(
	context.Context, int,
) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *setMDErrSource) ListAuditFiltered(
	context.Context, domain.AuditFilter, int,
) ([]domain.AuditRow, error) {
	return nil, nil
}

func (s *setMDErrSource) CheckOrder(
	context.Context, domain.OrderProbe,
) (domain.CheckResult, error) {
	return domain.CheckResult{}, nil
}

func (s *setMDErrSource) SetMarketDataInstrumentEnabled(
	context.Context, string, string, bool,
) error {
	return s.setMDErr
}

func (s *setMDErrSource) CommandEnabled(context.Context, string) (bool, error) {
	return true, nil
}

func (s *setMDErrSource) SubmitOrderToken(
	context.Context, domain.Order, string, domain.MissingAccountPolicy,
) (SubmitOrderTokenResult, error) {
	return SubmitOrderTokenResult{}, nil
}

func (s *setMDErrSource) SubmitDropCopyOrder(
	context.Context, domain.Order, domain.MissingAccountPolicy,
) (SubmitDropCopyOrderResult, error) {
	return SubmitDropCopyOrderResult{}, nil
}

func (s *setMDErrSource) ConfirmExecution(
	context.Context, string, string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

func (s *setMDErrSource) CancelOrder(
	context.Context, string, string, string, string,
) (domain.Order, Attestation, error) {
	return domain.Order{}, Attestation{}, nil
}

// TestSetMarketDataInstrumentSourceFailure covers the path where the gate
// passes, both required fields are present, but the source mutation returns an
// error: the handler must surface the "set market-data instrument failed" tool
// error.
func TestSetMarketDataInstrumentSourceFailure(t *testing.T) {
	t.Parallel()
	src := &setMDErrSource{setMDErr: errors.New("store write rejected")}
	res := callSetMarketDataInstrument(t, src, setMarketDataInstrumentInput{
		InstanceExternalID: "mock-1", ExternalSymbol: "AAPL", Enabled: true,
	})
	requireToolError(t, res.IsError)
	if got := textContent(res.Content); got != "set market-data instrument failed" {
		t.Fatalf("unexpected error text: %q", got)
	}
}

// -- NewServer nil-source guard --

// TestNewServerNilSource verifies the nil-source guard: NewServer rejects a nil
// Source with an error naming the package, before any tool registration.
func TestNewServerNilSource(t *testing.T) {
	t.Parallel()
	srv, err := NewServer(nil, nil)
	if err == nil {
		t.Fatal("NewServer(nil, ...) must return an error")
	}
	if srv != nil {
		t.Fatalf("NewServer(nil, ...) must return a nil server, got %v", srv)
	}
	if !strings.Contains(err.Error(), "mcp: nil source") {
		t.Fatalf("error should contain %q, got %q", "mcp: nil source", err.Error())
	}
}

// -- Handler factory smoke test --

// TestHandlerReturnsHTTPHandler is a serve-mode factory smoke test: Handler
// builds the server and the streamable-HTTP handler entirely in memory (no
// network listener, no real I/O), so it must return a non-nil http.Handler and
// no error.
func TestHandlerReturnsHTTPHandler(t *testing.T) {
	t.Parallel()
	h, err := Handler(&fakeSource{}, nil)
	if err != nil {
		t.Fatalf("Handler returned error: %v", err)
	}
	if h == nil {
		t.Fatal("Handler returned a nil http.Handler")
	}
}

// TestHandlerNilSource verifies the factory propagates the nil-source guard
// from NewServer instead of returning a usable handler.
func TestHandlerNilSource(t *testing.T) {
	t.Parallel()
	h, err := Handler(nil, nil)
	if err == nil {
		t.Fatal("Handler(nil, ...) must return an error")
	}
	if h != nil {
		t.Fatalf("Handler(nil, ...) must return a nil handler, got %v", h)
	}
	if !strings.Contains(err.Error(), "mcp: nil source") {
		t.Fatalf("error should contain %q, got %q", "mcp: nil source", err.Error())
	}
}
