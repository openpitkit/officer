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

package trading

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/scmhub/ibapi"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/ib"
)

const (
	// Derived feed ids occupy 1001..900999 and search adds 10_000_000.
	// Trading adds 20_000_000; probes add another 20_000_000, so all
	// derived ranges are disjoint. Explicit ids remain the operator's choice.
	ibTradingClientIDOffset int64 = 20_000_000
	ibProbeClientIDOffset   int64 = 20_000_000
	ibTradingTimeout              = 10 * time.Second
)

type ibTradingClient interface {
	ib.Client
	TWSConnectionTime() string
	ReqManagedAccts()
	ReqContractDetails(int64, *ibapi.Contract)
	PlaceOrder(ibapi.OrderID, *ibapi.Contract, *ibapi.Order)
	ReqOpenOrders()
	ReqCompletedOrders(bool)
	ReqExecutions(int64, *ibapi.ExecutionFilter)
}

type ibTradingLiveClient struct{ *ibapi.EClient }

func (c ibTradingLiveClient) ForceDisconnect() error {
	return ib.ForceDisconnect(c.EClient)
}

type ibTradingCredentials struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	ClientID *int64 `json:"clientId"`
}

type ibConnector struct {
	credentials  ibTradingCredentials
	clientID     int64
	mode         domain.TradingMode
	newClient    func(ibapi.EWrapper) ibTradingClient
	timeout      time.Duration
	reportStatus health.StatusReporter
	reportDiag   health.DiagnosticReporter
	mu           sync.Mutex
	closed       bool
	ctx          context.Context
	cancel       context.CancelFunc
	lifecycle    streamLifecycle
	session      *ibTradingSession
	probes       sync.WaitGroup
}

var (
	_ fwtrading.Connector        = (*ibConnector)(nil)
	_ fwtrading.PreChecker       = (*ibConnector)(nil)
	_ fwtrading.AccountChecker   = (*ibConnector)(nil)
	_ fwtrading.SymbolVerifier   = (*ibConnector)(nil)
	_ fwtrading.SymbolSearcher   = (*ibConnector)(nil)
	_ health.StatusReporting     = (*ibConnector)(nil)
	_ health.DiagnosticReporting = (*ibConnector)(nil)
)

// NewIBConnector validates explicit TWS settings without contacting the venue.
// The subscription owns the trading session; discovery uses serialized probes.
func NewIBConnector(connection domain.TradingConnection) (fwtrading.Connector, error) {
	if connection.Mode != domain.TradingModePaper && connection.Mode != domain.TradingModeLive {
		return nil, errors.New("ib: unsupported trading mode")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal([]byte(connection.Credentials), &fields) != nil || fields == nil {
		return nil, errors.New("ib: invalid credentials JSON")
	}
	for field := range fields {
		if field != "host" && field != "port" && field != "clientId" {
			return nil, errors.New("ib: unknown credentials field")
		}
	}
	var credentials ibTradingCredentials
	if json.Unmarshal([]byte(connection.Credentials), &credentials) != nil {
		return nil, errors.New("ib: invalid host, port or clientId")
	}
	if strings.TrimSpace(credentials.Host) == "" {
		return nil, errors.New("ib: host is required")
	}
	if credentials.Port < 1 || credentials.Port > 65535 {
		return nil, errors.New("ib: port is required and must be in 1..65535")
	}
	clientID := ib.ClientID(string(connection.ExternalID)) + ibTradingClientIDOffset
	if raw, exists := fields["clientId"]; exists {
		if string(raw) == "null" || credentials.ClientID == nil || *credentials.ClientID < 0 || *credentials.ClientID > math.MaxInt32-ibProbeClientIDOffset {
			return nil, errors.New("ib: clientId must allow a distinct probe id within int32")
		}
		clientID = *credentials.ClientID
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &ibConnector{
		credentials: credentials, clientID: clientID, mode: connection.Mode,
		ctx: ctx, cancel: cancel, timeout: ibTradingTimeout,
		newClient: func(w ibapi.EWrapper) ibTradingClient {
			return ibTradingLiveClient{ibapi.NewEClient(w)}
		},
	}, nil
}

// SetStatusReporter installs the stream health callback before Subscribe.
func (c *ibConnector) SetStatusReporter(report health.StatusReporter) { c.reportStatus = report }

// SetDiagnosticReporter installs the venue diagnostic callback before use.
func (c *ibConnector) SetDiagnosticReporter(report health.DiagnosticReporter) { c.reportDiag = report }

func (c *ibConnector) diagnostic(code, detail string) {
	if c.reportDiag != nil {
		c.reportDiag(health.Diagnostic{
			Level: health.DiagError, Kind: health.DiagKindProvider,
			Code: code, Title: "Interactive Brokers trading data unavailable",
			Detail:      detail,
			Remediation: "Compare TWS executions with Officer before resolving the pending order.",
		})
	}
}

func (c *ibConnector) liveSession() (*ibTradingSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.session == nil {
		return nil, errors.New("ib: no ready trading session")
	}
	select {
	case <-c.session.ctx.Done():
		return nil, errors.New("ib: trading session ended")
	default:
		return c.session, nil
	}
}

func (c *ibConnector) checkMode(accounts []string) error {
	if len(accounts) == 0 {
		return errors.New("ib: no managed accounts")
	}
	for _, account := range accounts {
		paper := strings.HasPrefix(account, "D")
		if c.mode == domain.TradingModePaper && !paper {
			return errors.New("ib: paper mode found a live account")
		}
		if c.mode == domain.TradingModeLive && paper {
			return errors.New("ib: live mode found a paper account")
		}
	}
	return nil
}

// CheckAccount checks every managed account against the explicitly selected mode.
func (c *ibConnector) CheckAccount(ctx context.Context) (info fwtrading.AccountInfo, err error) {
	err = c.probe(ctx, func(ctx context.Context, s *ibTradingSession) error {
		accounts, err := s.accounts(ctx)
		if err != nil {
			return err
		}
		if err := c.checkMode(accounts); err != nil {
			return err
		}
		sort.Strings(accounts)
		info = fwtrading.AccountInfo{Identity: strings.Join(accounts, ","), Status: "active"}
		return nil
	})
	return
}
