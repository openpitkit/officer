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

package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/scmhub/ibapi"
)

const (
	defaultIBHost                   = "127.0.0.1"
	defaultIBPort                   = 7496
	defaultIBSecType                = "STK"
	defaultIBExchange               = "SMART"
	defaultIBReconnectMin           = 500 * time.Millisecond
	defaultIBReconnectMax           = 10 * time.Second
	defaultIBReadyTimeout           = 10 * time.Second
	defaultIBConnectTimeout         = 10 * time.Second
	defaultIBConnectDrainTimeout    = time.Second
	defaultIBForceDisconnectTimeout = 5 * time.Second
	// Hashed instance IDs start above this value to avoid fallback collisions.
	defaultIBFallbackClientID       = 1000
	ibapiDisabledLogLevel           = 7
	defaultIBMarketDataType   int64 = int64(ibapi.REALTIME)
	// Symbol search runs on a short-lived connection whose clientId is offset far
	// above any feed clientId (feed ids stay <= ~900_999) so the two never
	// collide on the same TWS/Gateway session.
	ibSearchClientIDOffset int64 = 10_000_000
	defaultIBSearchTimeout       = 5 * time.Second
	ibSearchReqID          int64 = 1
)

type ibConnector struct {
	newClient  func(ibapi.EWrapper) ibClient
	sleep      func(context.Context, time.Duration) error
	report     StatusReporter
	diagReport DiagnosticReporter
	now        func() time.Time

	cfg            ibConfig
	configErr      error
	reconnectMin   time.Duration
	reconnectMax   time.Duration
	readyTimeout   time.Duration
	connectTimeout time.Duration
	searchTimeout  time.Duration

	subsMu sync.Mutex
	subs   []ibSubscription
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type ibClient interface {
	Connect(host string, port int, clientID int64) error
	Disconnect() error
	ReqMktData(
		reqID ibapi.TickerID,
		contract *ibapi.Contract,
		genericTickList string,
		snapshot bool,
		regulatorySnapshot bool,
		mktDataOptions []ibapi.TagValue,
	)
	CancelMktData(reqID ibapi.TickerID)
	ReqMarketDataType(marketDataType int64)
	ReqContractDetails(reqID int64, contract *ibapi.Contract)
}

// IB partial-connect teardown via reflect+unsafe is a deliberate, documented
// workaround for scmhub/ibapi v0.10.44, not a pattern to copy.
//
// Why no public path exists: EClient.Connect starts the EReader
// scanner/decoder and the requester goroutines and opens the TCP socket BEFORE
// startAPI(). On a handshake/startAPI failure Connect returns an error with
// connState left at CONNECTING. The public Disconnect() early-returns on
// !IsConnected() (which requires CONNECTED), so it is a no-op in that state,
// and the scanner goroutine stays blocked in scanner.Scan() - only a physical
// socket close unblocks it. Nothing needed for teardown is exported: the cancel
// func, *Connection, connState, the tcpConn pointer, the isConnected flag and
// the WaitGroup are all unexported; NewEClient takes only an EWrapper (no
// net.Conn/dialer injection); neither Connect nor
// ConnectWithGracefulShutdown accepts a context. Confirmed absent in v0.10.44
// through v0.10.46. So we drive the library's own cancel -> close socket ->
// wg.Wait sequence by reaching the private fields.
//
// What this is bound to: the exact unexported field/method names of
// EClient/Connection in v0.10.44 - cancel, conn, connState, tcpConn,
// isConnected, wg. If any is renamed or removed in a newer ibapi, these helpers
// degrade to a logged errors.Join error instead of tearing down: the socket and
// goroutines then leak again, silently. Treat the ibapi version as effectively
// pinned to this hack; an upgrade is not safe without re-validating the fields.
//
// On every ibapi upgrade, re-check and switch to the clean path if it appears -
// any one of these removes the need for reflect/unsafe and lets this whole
// block be deleted in favor of a plain Disconnect():
//   - Connect cleans up after itself on partial failure (cancels goroutines and
//     closes the socket before returning the error);
//   - an exported ForceDisconnect()/Close(), or a Disconnect() that works
//     regardless of connState;
//   - a context-aware Connect, or a constructor/option that accepts a
//     net.Conn/dialer so officer owns and can close the socket itself.
type ibForceDisconnecter interface {
	ForceDisconnect() error
}

type liveIBClient struct {
	client *ibapi.EClient
}

type ibConfig struct {
	ContractDefaults ibContractConfig
	Contracts        map[string]ibContractConfig
	Host             string
	ClientID         int64
	MarketDataType   int64
	Port             int
}

type ibCredentialJSON struct {
	Host           string                      `json:"host"`
	MarketDataType any                         `json:"marketDataType"`
	Contracts      map[string]ibContractConfig `json:"contracts"`
	ClientID       int64                       `json:"clientId"`
	Port           int                         `json:"port"`
}

type ibContractConfig struct {
	Symbol                       string `json:"symbol"`
	SecType                      string `json:"secType"`
	LastTradeDateOrContractMonth string `json:"lastTradeDateOrContractMonth"`
	Right                        string `json:"right"`
	Multiplier                   string `json:"multiplier"`
	Exchange                     string `json:"exchange"`
	PrimaryExchange              string `json:"primaryExchange"`
	Currency                     string `json:"currency"`
	LocalSymbol                  string `json:"localSymbol"`
	TradingClass                 string `json:"tradingClass"`
	SecIDType                    string `json:"secIdType"`
	SecID                        string `json:"secId"`
	ConID                        string `json:"conId"`
	Strike                       string `json:"strike"`
	IncludeExpired               bool   `json:"includeExpired"`
}

type ibSubscription struct {
	Subscription
	contract *ibapi.Contract
	reqID    ibapi.TickerID
}

type ibPartialQuote struct {
	Subscription
	lastTradeMark string
	computedMark  string
	sourceAsOf    time.Time
	Mark          string
	Bid           string
	Ask           string
}

type ibWrapper struct {
	ibapi.Wrapper

	reportStatus func(bool, string)
	reportDiag   func(Diagnostic)
	now          func() time.Time

	readyOnce sync.Once
	lostOnce  sync.Once
	// delivered records whether ≥1 quote reached out, so run can reset backoff.
	delivered atomic.Bool
	mu        sync.Mutex
	quotes    map[ibapi.TickerID]ibPartialQuote
	subs      map[ibapi.TickerID]ibSubscription
	noData    map[string]struct{}
	ready     chan struct{}
	lost      chan error
	ctx       context.Context
	out       chan<- QuoteUpdate
}

func init() {
	ibapi.SetLogLevel(ibapiDisabledLogLevel)
}

// NewIBConnector creates an Interactive Brokers TWS/Gateway market-data connector.
func NewIBConnector(instanceID, credentials string) *ibConnector {
	cfg, err := parseIBConfig(instanceID, credentials)
	return &ibConnector{
		newClient: func(wrapper ibapi.EWrapper) ibClient {
			return liveIBClient{client: ibapi.NewEClient(wrapper)}
		},
		sleep:          sleepContext,
		now:            func() time.Time { return time.Now().UTC() },
		cfg:            cfg,
		configErr:      err,
		reconnectMin:   defaultIBReconnectMin,
		reconnectMax:   defaultIBReconnectMax,
		readyTimeout:   defaultIBReadyTimeout,
		connectTimeout: defaultIBConnectTimeout,
		searchTimeout:  defaultIBSearchTimeout,
	}
}

func (c liveIBClient) Connect(host string, port int, clientID int64) error {
	return c.client.Connect(host, port, clientID)
}

func (c liveIBClient) Disconnect() error {
	return c.ForceDisconnect()
}

func (c liveIBClient) ForceDisconnect() error {
	return forceDisconnectIBClient(c.client)
}

func (c liveIBClient) ReqMktData(
	reqID ibapi.TickerID,
	contract *ibapi.Contract,
	genericTickList string,
	snapshot bool,
	regulatorySnapshot bool,
	mktDataOptions []ibapi.TagValue,
) {
	c.client.ReqMktData(
		reqID,
		contract,
		genericTickList,
		snapshot,
		regulatorySnapshot,
		mktDataOptions,
	)
}

func (c liveIBClient) CancelMktData(reqID ibapi.TickerID) {
	c.client.CancelMktData(reqID)
}

func (c liveIBClient) ReqMarketDataType(marketDataType int64) {
	c.client.ReqMarketDataType(marketDataType)
}

func (c liveIBClient) ReqContractDetails(reqID int64, contract *ibapi.Contract) {
	c.client.ReqContractDetails(reqID, contract)
}

func disconnectIBClient(client ibClient) error {
	if force, ok := client.(ibForceDisconnecter); ok {
		return force.ForceDisconnect()
	}
	return client.Disconnect()
}

func forceDisconnectIBClient(client *ibapi.EClient) error {
	if client == nil {
		return nil
	}
	value := reflect.ValueOf(client)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return nil
	}
	elem := value.Elem()
	return errors.Join(
		cancelIBClient(elem),
		closeIBClientSocket(elem),
		waitIBClientShutdown(elem),
	)
}

// Touches EClient.cancel (context.CancelFunc).
func cancelIBClient(client reflect.Value) error {
	cancelField := client.FieldByName("cancel")
	if !cancelField.IsValid() {
		return errors.New("ib: ibapi EClient.cancel field not found")
	}
	if cancelField.IsNil() {
		return nil
	}
	unsafeReflectValue(cancelField).Call(nil)
	return nil
}

// Touches EClient.conn, Connection.tcpConn, and Connection.isConnected.
func closeIBClientSocket(client reflect.Value) error {
	setIBInt32Field(client, "connState", int32(ibapi.DISCONNECTED))

	connField := client.FieldByName("conn")
	if !connField.IsValid() {
		return errors.New("ib: ibapi EClient.conn field not found")
	}
	if connField.IsNil() {
		return nil
	}
	conn := unsafeReflectValue(connField)
	connElem := conn.Elem()
	setIBInt32Field(connElem, "isConnected", 0)

	tcpField := connElem.FieldByName("tcpConn")
	if !tcpField.IsValid() {
		return errors.New("ib: ibapi Connection.tcpConn field not found")
	}
	swap := unsafeReflectValue(tcpField).Addr().MethodByName("Swap")
	if !swap.IsValid() {
		return errors.New("ib: ibapi Connection.tcpConn swap method not found")
	}
	result := swap.Call([]reflect.Value{
		reflect.Zero(reflect.TypeOf((*net.TCPConn)(nil))),
	})
	if len(result) != 1 || result[0].IsNil() {
		return nil
	}
	tcpConn, ok := result[0].Interface().(*net.TCPConn)
	if !ok {
		return errors.New("ib: ibapi Connection.tcpConn has unexpected type")
	}
	return tcpConn.Close()
}

// Touches EClient.wg, bounded by defaultIBForceDisconnectTimeout.
func waitIBClientShutdown(client reflect.Value) error {
	wgField := client.FieldByName("wg")
	if !wgField.IsValid() {
		return errors.New("ib: ibapi EClient.wg field not found")
	}
	wg := unsafeReflectValue(wgField)
	wait := wg.Addr().MethodByName("Wait")
	if !wait.IsValid() {
		return errors.New("ib: ibapi EClient.wg wait method not found")
	}
	done := make(chan struct{})
	go func() {
		wait.Call(nil)
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-time.After(defaultIBForceDisconnectTimeout):
		return errors.New("ib: timed out waiting for ibapi shutdown")
	}
}

// Generic private int32 access used for connState and isConnected.
func setIBInt32Field(value reflect.Value, name string, next int32) {
	field := value.FieldByName(name)
	if !field.IsValid() {
		return
	}
	atomic.StoreInt32((*int32)(unsafe.Pointer(field.UnsafeAddr())), next)
}

// Generic addressable access used for connState and isConnected.
func unsafeReflectValue(value reflect.Value) reflect.Value {
	return reflect.NewAt(value.Type(), unsafe.Pointer(value.UnsafeAddr())).Elem()
}

// SetStatusReporter installs the runtime connection status callback.
func (c *ibConnector) SetStatusReporter(report StatusReporter) {
	c.report = report
}

func (c *ibConnector) reportStatus(ok bool, errMsg string) {
	if c.report != nil {
		c.report(ok, errMsg)
	}
}

// SetDiagnosticReporter installs the structured diagnostic callback.
func (c *ibConnector) SetDiagnosticReporter(report DiagnosticReporter) {
	c.diagReport = report
}

func (c *ibConnector) reportDiag(diag Diagnostic) {
	if c.diagReport != nil {
		c.diagReport(diag)
	}
}

// Diagnose returns no-data findings for the active IB subscriptions.
func (c *ibConnector) Diagnose(context.Context) ([]Diagnostic, error) {
	c.subsMu.Lock()
	subs := append([]ibSubscription(nil), c.subs...)
	c.subsMu.Unlock()

	findings := make([]Diagnostic, 0, len(subs))
	for _, sub := range subs {
		findings = append(findings, Diagnostic{
			Level:       DiagWarn,
			Code:        CodeNoData,
			Kind:        DiagKindProvider,
			Title:       "No IB quote received",
			Detail:      "Interactive Brokers has not emitted a quote for this subscription.",
			Remediation: "Check TWS/Gateway contract resolution, market-data permissions, and the requested marketDataType.",
			Instrument:  sub.External,
			Actions:     []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
		})
	}
	return findings, nil
}

// References returns operator documentation links for the IB provider.
func (c *ibConnector) References() (ProviderReferences, bool) {
	return ProviderReferences{
		DocsURL: "https://interactivebrokers.github.io/tws-api/md_request.html",
	}, true
}

// Subscribe starts streaming normalized quotes for the requested subscriptions.
func (c *ibConnector) Subscribe(
	ctx context.Context, subs []Subscription,
) (<-chan QuoteUpdate, error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	normalized, err := normalizeIBSubscriptions(c.cfg, subs)
	if err != nil {
		return nil, err
	}
	c.subsMu.Lock()
	c.subs = append(c.subs[:0], normalized...)
	c.subsMu.Unlock()

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel

	out := make(chan QuoteUpdate)
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer close(out)
		c.run(runCtx, normalized, out)
	}()
	return out, nil
}

// SearchSymbols opens a short-lived TWS connection (distinct clientId), issues
// ReqContractDetails requests for the query's likely IB contracts, accumulates
// each returned ContractDetails until ContractDetailsEnd or a brief timeout,
// and disconnects. It never subscribes and never touches the running feed, so
// it is safe to call against a live instance. Caller-supplied criteria (e.g. the
// strike) are validated at the API boundary, so a non-nil error reports a
// connect/transport/provider failure; an IB "no security definition" (code 200),
// a timeout, and a zero-match resolve all return a nil slice with a nil error.
func (c *ibConnector) SearchSymbols(
	ctx context.Context, query SymbolSearchQuery,
) (result []SymbolMatch, err error) {
	if c.configErr != nil {
		return nil, c.configErr
	}
	pattern := strings.TrimSpace(query.Query)
	if pattern == "" {
		return nil, nil
	}
	contracts, err := ibSearchContracts(query)
	if err != nil {
		return nil, err
	}

	results := make(chan ibSearchResult, len(contracts))
	errs := make(chan error, 1)
	wrapper := newIBSearchWrapper(results, errs)
	client := c.newClient(wrapper)
	defer func() {
		if disconnectErr := disconnectIBClient(client); disconnectErr != nil {
			disconnectErr = fmt.Errorf(
				"ib: disconnect symbol search client: %w", disconnectErr,
			)
			if err != nil {
				err = errors.Join(err, disconnectErr)
				return
			}
			// Registry searches use a fresh connector without a DiagnosticReporter,
			// so this cleanup failure stays on the process logger.
			slog.Error(
				"disconnect ib symbol search client",
				"query", pattern,
				"client_id", c.cfg.ClientID+ibSearchClientIDOffset,
				"error", disconnectErr,
			)
		}
	}()

	clientID := c.cfg.ClientID + ibSearchClientIDOffset
	if err := c.connectClient(ctx, client, clientID); err != nil {
		return nil, ibConnectError(c.cfg, err)
	}
	if err := wrapper.waitReady(ctx, c.readyTimeout); err != nil {
		return nil, err
	}

	reqID := ibSearchReqID
	for _, contract := range contracts {
		client.ReqContractDetails(reqID, contract)
		matches, err := wrapper.waitResult(ctx, reqID, c.searchTimeout)
		if err != nil {
			return nil, err
		}
		if len(matches) > 0 {
			return matches, nil
		}
		reqID++
	}
	return nil, nil
}

func ibSearchContracts(query SymbolSearchQuery) ([]*ibapi.Contract, error) {
	pattern := strings.TrimSpace(query.Query)
	if pattern == "" {
		return nil, nil
	}
	if ibSearchQueryHasCriteria(query) {
		contract, err := ibSearchContractFromQuery(query)
		if err != nil {
			return nil, err
		}
		return []*ibapi.Contract{contract}, nil
	}
	if base, quote, ok := splitSymbolPair(pattern); ok {
		return []*ibapi.Contract{
			ibSearchContract(base, "CASH", "IDEALPRO", quote, query),
			ibSearchContract(base, "CRYPTO", "PAXOS", quote, query),
		}, nil
	}
	return []*ibapi.Contract{
		ibSearchContract(pattern, "CASH", "IDEALPRO", "USD", query),
		ibSearchContract(pattern, "CRYPTO", "PAXOS", "USD", query),
		ibSearchContract(pattern, "STK", "SMART", "USD", query),
	}, nil
}

func ibSearchQueryHasCriteria(query SymbolSearchQuery) bool {
	return strings.TrimSpace(query.SecType) != "" ||
		strings.TrimSpace(query.Exchange) != "" ||
		strings.TrimSpace(query.Currency) != "" ||
		strings.TrimSpace(query.LastTradeDateOrContractMonth) != "" ||
		strings.TrimSpace(query.Right) != "" ||
		strings.TrimSpace(query.Strike) != ""
}

func ibSearchContractFromQuery(query SymbolSearchQuery) (*ibapi.Contract, error) {
	symbol := strings.TrimSpace(query.Query)
	currency := strings.TrimSpace(query.Currency)
	if base, quote, ok := splitSymbolPair(symbol); ok {
		symbol = base
		if currency == "" {
			currency = quote
		}
	}
	contract := ibSearchContract(
		symbol,
		strings.TrimSpace(query.SecType),
		strings.TrimSpace(query.Exchange),
		currency,
		query,
	)
	strike, err := parseIBFloat64(query.Strike, "strike")
	if err != nil {
		return nil, err
	}
	contract.Strike = strike
	return contract, nil
}

func ibSearchContract(
	symbol, secType, exchange, currency string,
	query SymbolSearchQuery,
) *ibapi.Contract {
	return &ibapi.Contract{
		Symbol:                       strings.TrimSpace(symbol),
		SecType:                      strings.TrimSpace(secType),
		Exchange:                     strings.TrimSpace(exchange),
		Currency:                     strings.TrimSpace(currency),
		LastTradeDateOrContractMonth: strings.TrimSpace(query.LastTradeDateOrContractMonth),
		Right:                        strings.TrimSpace(query.Right),
	}
}

func (c *ibConnector) run(
	ctx context.Context, subs []ibSubscription, out chan<- QuoteUpdate,
) {
	attempt := 0
	for {
		delivered, err := c.stream(ctx, subs, out)
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		// A healthy connection that dropped reconnects from the minimum delay.
		if delivered {
			attempt = 0
		}
		c.reportStatus(false, err.Error())
		attempt++
		if err := c.sleep(ctx, backoff(attempt, c.reconnectMin, c.reconnectMax)); err != nil {
			return
		}
	}
}

// stream runs one connection attempt; the bool reports whether ≥1 quote was
// delivered, so run resets its reconnect backoff after a healthy connection.
func (c *ibConnector) stream(
	ctx context.Context, subs []ibSubscription, out chan<- QuoteUpdate,
) (delivered bool, err error) {
	wrapper := newIBWrapper(ctx, subs, out, c.now, c.reportStatus, c.reportDiag)
	client := c.newClient(wrapper)
	defer func() {
		for _, sub := range subs {
			client.CancelMktData(sub.reqID)
		}
		if disconnectErr := disconnectIBClient(client); disconnectErr != nil {
			disconnectErr = fmt.Errorf(
				"ib: disconnect market data client: %w", disconnectErr,
			)
			if err != nil && !errors.Is(err, context.Canceled) {
				err = errors.Join(err, disconnectErr)
			}
			c.reportDiag(Diagnostic{
				Level:       DiagError,
				Code:        CodeConnectionError,
				Kind:        DiagKindProvider,
				Title:       "Interactive Brokers disconnect failed",
				Detail:      disconnectErr.Error(),
				Remediation: "Check that TWS/Gateway released the client ID, then Restart feeds.",
				Actions:     []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
			})
		}
	}()
	if err := c.connect(ctx, client); err != nil {
		return wrapper.delivered.Load(), ibConnectError(c.cfg, err)
	}

	if err := wrapper.waitReady(ctx, c.readyTimeout); err != nil {
		return wrapper.delivered.Load(), err
	}
	if c.cfg.MarketDataType > 0 {
		client.ReqMarketDataType(c.cfg.MarketDataType)
	}
	for _, sub := range subs {
		client.ReqMktData(sub.reqID, sub.contract, "", false, false, nil)
	}
	c.reportStatus(true, "")

	select {
	case <-ctx.Done():
		return wrapper.delivered.Load(), ctx.Err()
	case err := <-wrapper.lost:
		return wrapper.delivered.Load(), err
	}
}

func (c *ibConnector) connect(ctx context.Context, client ibClient) error {
	return c.connectClient(ctx, client, c.cfg.ClientID)
}

func (c *ibConnector) connectClient(
	ctx context.Context, client ibClient, clientID int64,
) error {
	if c.connectTimeout <= 0 {
		return client.Connect(c.cfg.Host, c.cfg.Port, clientID)
	}

	connectCtx, cancel := context.WithTimeout(ctx, c.connectTimeout)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		result <- client.Connect(c.cfg.Host, c.cfg.Port, clientID)
	}()

	select {
	case err := <-result:
		return err
	case <-connectCtx.Done():
		disconnectErr := disconnectIBClient(client)
		select {
		case <-result:
		case <-time.After(defaultIBConnectDrainTimeout):
		}
		if disconnectErr != nil {
			return errors.Join(
				connectCtx.Err(),
				fmt.Errorf("ib: disconnect timed-out client: %w", disconnectErr),
			)
		}
		return connectCtx.Err()
	}
}

// Close stops the subscription stream and waits for cleanup to finish.
func (c *ibConnector) Close() {
	c.once.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
	})
	c.wg.Wait()
	c.subsMu.Lock()
	c.subs = nil
	c.subsMu.Unlock()
}

func newIBWrapper(
	ctx context.Context,
	subs []ibSubscription,
	out chan<- QuoteUpdate,
	now func() time.Time,
	reportStatus func(bool, string),
	reportDiag func(Diagnostic),
) *ibWrapper {
	subMap := make(map[ibapi.TickerID]ibSubscription, len(subs))
	quotes := make(map[ibapi.TickerID]ibPartialQuote, len(subs))
	for _, sub := range subs {
		subMap[sub.reqID] = sub
		quotes[sub.reqID] = ibPartialQuote{Subscription: sub.Subscription}
	}
	return &ibWrapper{
		reportStatus: reportStatus,
		reportDiag:   reportDiag,
		now:          now,
		quotes:       quotes,
		subs:         subMap,
		noData:       make(map[string]struct{}),
		ready:        make(chan struct{}),
		lost:         make(chan error, 1),
		ctx:          ctx,
		out:          out,
	}
}

func (w *ibWrapper) waitReady(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ready:
		return nil
	case <-timer.C:
		return errors.New("ib: timed out waiting for TWS/Gateway API readiness")
	}
}

func (w *ibWrapper) NextValidID(int64) {
	w.readyOnce.Do(func() { close(w.ready) })
}

func (w *ibWrapper) ConnectAck() {}

func (w *ibWrapper) ConnectionClosed() {
	w.signalLost(errors.New("ib: TWS/Gateway connection closed"))
}

func (w *ibWrapper) Error(
	reqID ibapi.TickerID, _ int64, code int64, message string, _ string,
) {
	detail := strings.TrimSpace(message)
	if detail == "" {
		detail = fmt.Sprintf("IB API error %d", code)
	}
	if ibFatalError(code) {
		w.signalLost(fmt.Errorf("ib: %s", detail))
		return
	}

	diag := Diagnostic{
		Level:       DiagError,
		Code:        CodeConnectionError,
		Kind:        DiagKindProvider,
		Title:       "Interactive Brokers error",
		Detail:      fmt.Sprintf("IB API error %d: %s", code, detail),
		Remediation: "Check the TWS/Gateway API settings, market-data permissions, and contract configuration.",
		Actions:     []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
	}
	switch {
	case ibInformationalError(code):
		diag.Level = DiagInfo
		diag.Routine = true
		diag.Title = "IB connection status"
		diag.Remediation = ""
		diag.Actions = nil
	case ibRecoverableConnectionError(code):
		diag.Level = DiagWarn
		diag.Title = "IB connection interrupted"
		diag.Remediation = "Wait for TWS/Gateway connectivity to recover, or Restart feeds if the status does not clear."
	case ibMarketDataUnavailableError(code):
		diag.Level = DiagWarn
		diag.Code = CodeNoData
		diag.Kind = DiagKindConfig
		diag.Title = "IB market data unavailable"
		diag.Remediation = "Check account entitlements and the requested market-data type, then Restart feeds."
	case code == 326:
		diag.Level = DiagWarn
		diag.Kind = DiagKindConfig
		diag.Title = "IB client ID already in use"
		diag.Remediation = "Configure a unique clientId for this Officer IB provider instance, then Restart feeds."
	}
	if sub, ok := w.subscription(reqID); ok {
		diag.Instrument = sub.External
		if code == 200 {
			diag.Code = CodeUnknownSymbol
			diag.Kind = DiagKindConfig
			diag.Title = "IB contract not found"
			diag.Detail = fmt.Sprintf("IB did not resolve contract %q: %s", sub.External, detail)
			diag.Remediation = "Adjust the external symbol or the IB contract override, then Restart feeds."
			diag.Actions = []DiagnosticAction{
				{Type: ActionRemoveInstrument, Target: sub.External},
				{Type: ActionOpenDocs},
			}
		}
	}
	w.reportDiag(diag)
}

func (w *ibWrapper) TickPrice(
	reqID ibapi.TickerID, tickType ibapi.TickType, price float64, _ ibapi.TickAttrib,
) {
	field, ok := ibTickField(tickType)
	if !ok || math.IsNaN(price) || math.IsInf(price, 0) {
		return
	}
	if price == -1 {
		if update, ok := w.clearQuoteField(reqID, field); ok {
			w.emit(update)
		}
		w.reportTickNoData(reqID, tickType)
		return
	}
	if price < 0 {
		return
	}
	if price == 0 && (field == "bid" || field == "ask") {
		if update, ok := w.clearQuoteField(reqID, field); ok {
			w.emit(update)
		}
		return
	}

	// IB exposes tick prices as float64; keep the shortest round-trip decimal
	// rather than adding precision that did not come from the provider.
	priceString := strconv.FormatFloat(price, 'f', -1, 64)
	if update, ok := w.setQuoteField(reqID, tickType, field, priceString); ok {
		w.emit(update)
	}
}

func (w *ibWrapper) TickString(reqID ibapi.TickerID, tickType ibapi.TickType, value string) {
	if tickType != ibapi.LAST_TIMESTAMP && tickType != ibapi.DELAYED_LAST_TIMESTAMP {
		return
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || seconds <= 0 {
		return
	}
	w.mu.Lock()
	partial, ok := w.quotes[reqID]
	if !ok {
		w.mu.Unlock()
		return
	}
	partial.sourceAsOf = time.Unix(seconds, 0).UTC()
	w.quotes[reqID] = partial
	w.mu.Unlock()
}

func (w *ibWrapper) setQuoteField(
	reqID ibapi.TickerID, tickType ibapi.TickType, field string, value string,
) (QuoteUpdate, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	partial, ok := w.quotes[reqID]
	if !ok {
		return QuoteUpdate{}, false
	}
	switch field {
	case "bid":
		partial.Bid = value
	case "ask":
		partial.Ask = value
	case "mark":
		if tickType == ibapi.MARK_PRICE {
			partial.computedMark = value
		} else {
			partial.lastTradeMark = value
		}
		partial.Mark = partial.lastTradeMark
		if partial.computedMark != "" {
			partial.Mark = partial.computedMark
		}
	}
	w.quotes[reqID] = partial
	return w.quoteUpdate(partial), true
}

func (w *ibWrapper) clearQuoteField(
	reqID ibapi.TickerID, field string,
) (QuoteUpdate, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	partial, ok := w.quotes[reqID]
	if !ok {
		return QuoteUpdate{}, false
	}
	switch field {
	case "bid":
		partial.Bid = ""
	case "ask":
		partial.Ask = ""
	case "mark":
		partial.lastTradeMark = ""
		partial.computedMark = ""
		partial.Mark = ""
	}
	w.quotes[reqID] = partial
	return w.quoteUpdate(partial), true
}

func (w *ibWrapper) quoteUpdate(partial ibPartialQuote) QuoteUpdate {
	asOf := partial.sourceAsOf
	if asOf.IsZero() {
		asOf = w.now()
	}
	return QuoteUpdate{
		AsOf:  asOf,
		Base:  partial.Base,
		Quote: partial.Quote,
		Mark:  partial.Mark,
		Bid:   partial.Bid,
		Ask:   partial.Ask,
	}
}

func (w *ibWrapper) reportTickNoData(reqID ibapi.TickerID, tickType ibapi.TickType) {
	key := fmt.Sprintf("%d:%d", reqID, tickType)
	w.mu.Lock()
	if _, ok := w.noData[key]; ok {
		w.mu.Unlock()
		return
	}
	w.noData[key] = struct{}{}
	sub, hasSub := w.subs[reqID]
	w.mu.Unlock()

	diag := Diagnostic{
		Level:       DiagWarn,
		Code:        CodeNoData,
		Kind:        DiagKindProvider,
		Title:       "IB tick has no data",
		Detail:      fmt.Sprintf("IB returned no data for %s.", ibapi.TickName(tickType)),
		Remediation: "Check IB market-data permissions and contract availability.",
		Actions:     []DiagnosticAction{{Type: ActionOpenDocs}},
	}
	if hasSub {
		diag.Instrument = sub.External
	}
	w.reportDiag(diag)
}

func (w *ibWrapper) emit(update QuoteUpdate) {
	if w.out == nil {
		return
	}
	select {
	case <-w.ctx.Done():
		return
	default:
	}

	// Record delivery before the send (as binance counts a parsed frame), so a
	// return that observes the flag never races the callback goroutine.
	w.delivered.Store(true)
	select {
	case <-w.ctx.Done():
	case w.out <- update:
	}
}

func (w *ibWrapper) MarketDataType(reqID ibapi.TickerID, marketDataType int64) {
	name := ibMarketDataTypeName(marketDataType)
	if name == "" {
		name = fmt.Sprintf("unknown(%d)", marketDataType)
	}
	if marketDataType == int64(ibapi.REALTIME) {
		w.reportDiag(Diagnostic{
			Level:      DiagInfo,
			Routine:    true,
			Code:       CodeDataFreshness,
			Kind:       DiagKindProvider,
			Title:      "IB market data type: realtime",
			Detail:     "Interactive Brokers is serving realtime market data.",
			Instrument: w.instrumentName(reqID),
		})
		return
	}
	w.reportDiag(Diagnostic{
		Level:       DiagWarn,
		Code:        CodeDataFreshness,
		Kind:        DiagKindProvider,
		Title:       "IB market data is not realtime",
		Detail:      "Interactive Brokers is serving " + name + " market data.",
		Remediation: "Check account entitlements or the requested marketDataType in provider credentials.",
		Instrument:  w.instrumentName(reqID),
		Actions:     []DiagnosticAction{{Type: ActionOpenDocs}},
	})
}

func (w *ibWrapper) subscription(reqID ibapi.TickerID) (ibSubscription, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sub, ok := w.subs[reqID]
	return sub, ok
}

func (w *ibWrapper) instrumentName(reqID ibapi.TickerID) string {
	sub, ok := w.subscription(reqID)
	if !ok {
		return ""
	}
	return sub.External
}

func (w *ibWrapper) signalLost(err error) {
	w.lostOnce.Do(func() {
		select {
		case w.lost <- err:
		default:
		}
	})
}

type ibSearchResult struct {
	matches []SymbolMatch
	reqID   int64
}

// ibSearchWrapper is the minimal EWrapper backing the stateless symbol resolve:
// it closes ready on NextValidID, accumulates ContractDetails under mu and
// flushes the slice per request on ContractDetailsEnd, maps IB code 200 ("no
// security definition") to an empty result for that request, and routes a fatal/
// duplicate-clientId/server-too-old error onto errs. It never subscribes or
// emits quotes.
type ibSearchWrapper struct {
	ibapi.Wrapper

	mu        sync.Mutex
	acc       map[int64][]SymbolMatch
	done      map[int64]struct{}
	readyOnce sync.Once
	errOnce   sync.Once
	ready     chan struct{}
	results   chan ibSearchResult
	errs      chan error
}

func newIBSearchWrapper(
	results chan ibSearchResult, errs chan error,
) *ibSearchWrapper {
	return &ibSearchWrapper{
		acc:     make(map[int64][]SymbolMatch),
		done:    make(map[int64]struct{}),
		ready:   make(chan struct{}),
		results: results,
		errs:    errs,
	}
}

func (w *ibSearchWrapper) waitReady(ctx context.Context, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-w.ready:
		return nil
	case <-timer.C:
		return errors.New("ib: timed out waiting for TWS/Gateway API readiness")
	}
}

func (w *ibSearchWrapper) NextValidID(int64) {
	w.readyOnce.Do(func() { close(w.ready) })
}

func (w *ibSearchWrapper) ConnectAck() {}

func (w *ibSearchWrapper) waitResult(
	ctx context.Context, reqID int64, timeout time.Duration,
) ([]SymbolMatch, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-w.errs:
			return nil, err
		case result := <-w.results:
			if result.reqID == reqID {
				return result.matches, nil
			}
		case <-timer.C:
			return nil, nil
		}
	}
}

func (w *ibSearchWrapper) ContractDetails(reqID int64, cd *ibapi.ContractDetails) {
	w.mu.Lock()
	w.acc[reqID] = append(w.acc[reqID], symbolMatchFromDetails(cd))
	w.mu.Unlock()
}

func (w *ibSearchWrapper) ContractDetailsEnd(reqID int64) {
	w.finish(reqID, nil)
}

func (w *ibSearchWrapper) finish(reqID int64, matches []SymbolMatch) {
	w.mu.Lock()
	if _, ok := w.done[reqID]; ok {
		w.mu.Unlock()
		return
	}
	w.done[reqID] = struct{}{}
	if matches == nil {
		matches = w.acc[reqID]
	}
	delete(w.acc, reqID)
	w.mu.Unlock()
	select {
	case w.results <- ibSearchResult{reqID: reqID, matches: matches}:
	default:
	}
}

func (w *ibSearchWrapper) Error(
	reqID ibapi.TickerID, _ int64, code int64, message string, _ string,
) {
	// IB code 200 ("No security definition has been found") means the resolve
	// found nothing, not a transport error: flush an empty result and stop.
	if code == 200 {
		w.finish(int64(reqID), nil)
		return
	}
	if !ibFatalError(code) && code != 326 && code != 503 {
		return
	}
	detail := strings.TrimSpace(message)
	if detail == "" {
		detail = fmt.Sprintf("IB API error %d", code)
	}
	w.errOnce.Do(func() {
		select {
		case w.errs <- fmt.Errorf("ib: %s", detail):
		default:
		}
	})
}

func symbolMatchFromDetails(cd *ibapi.ContractDetails) SymbolMatch {
	return SymbolMatch{
		Symbol:                       cd.Contract.Symbol,
		Name:                         cd.LongName,
		SecType:                      cd.Contract.SecType,
		Exchange:                     cd.Contract.Exchange,
		PrimaryExchange:              cd.Contract.PrimaryExchange,
		Currency:                     cd.Contract.Currency,
		LastTradeDateOrContractMonth: cd.Contract.LastTradeDateOrContractMonth,
		Right:                        cd.Contract.Right,
		Multiplier:                   cd.Contract.Multiplier,
		LocalSymbol:                  cd.Contract.LocalSymbol,
		TradingClass:                 cd.Contract.TradingClass,
		ConID:                        formatIBInt64(cd.Contract.ConID),
		Strike:                       formatIBFloat64(cd.Contract.Strike),
	}
}

func parseIBInt64(raw, field string) (int64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("ib contract %s: %w", field, err)
	}
	return value, nil
}

func parseIBFloat64(raw, field string) (float64, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return 0, fmt.Errorf("ib contract %s: %w", field, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("ib contract %s: non-finite value", field)
	}
	return value, nil
}

func formatIBInt64(value int64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatInt(value, 10)
}

func formatIBFloat64(value float64) string {
	if value == 0 {
		return ""
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func parseIBConfig(instanceID, raw string) (ibConfig, error) {
	cfg := ibConfig{
		Host:           defaultIBHost,
		Port:           defaultIBPort,
		ClientID:       defaultIBClientID(instanceID),
		MarketDataType: defaultIBMarketDataType,
		ContractDefaults: ibContractConfig{
			SecType:  defaultIBSecType,
			Exchange: defaultIBExchange,
		},
	}
	if strings.TrimSpace(raw) == "" {
		return cfg, nil
	}

	var input ibCredentialJSON
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		return cfg, fmt.Errorf("ib credentials: %w", err)
	}
	if strings.TrimSpace(input.Host) != "" {
		cfg.Host = strings.TrimSpace(input.Host)
	}
	if input.Port != 0 {
		if input.Port < 1 || input.Port > 65535 {
			return cfg, fmt.Errorf("ib credentials: invalid port %d", input.Port)
		}
		cfg.Port = input.Port
	}
	if input.ClientID != 0 {
		cfg.ClientID = input.ClientID
	}
	if input.MarketDataType != nil {
		typ, err := parseIBMarketDataType(input.MarketDataType)
		if err != nil {
			return cfg, err
		}
		cfg.MarketDataType = typ
	}
	cfg.Contracts = input.Contracts
	return cfg, nil
}

func defaultIBClientID(instanceID string) int64 {
	trimmed := strings.TrimSpace(instanceID)
	if trimmed == "" {
		return defaultIBFallbackClientID
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(trimmed))
	return defaultIBFallbackClientID + 1 + int64(hash.Sum32()%899999)
}

func parseIBMarketDataType(raw any) (int64, error) {
	switch value := raw.(type) {
	case float64:
		typ := int64(value)
		if float64(typ) != value {
			return 0, fmt.Errorf("ib credentials: invalid marketDataType %v", value)
		}
		return normalizeIBMarketDataType(typ)
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "live", "realtime", "real-time", "1":
			return int64(ibapi.REALTIME), nil
		case "frozen", "2":
			return int64(ibapi.FROZEN), nil
		case "delayed", "3":
			return int64(ibapi.DELAYED), nil
		case "delayed_frozen", "delayed-frozen", "4":
			return int64(ibapi.DELAYED_FROZEN), nil
		default:
			return 0, fmt.Errorf("ib credentials: invalid marketDataType %q", value)
		}
	default:
		return 0, fmt.Errorf("ib credentials: invalid marketDataType %T", raw)
	}
}

func normalizeIBMarketDataType(typ int64) (int64, error) {
	switch typ {
	case int64(ibapi.REALTIME), int64(ibapi.FROZEN),
		int64(ibapi.DELAYED), int64(ibapi.DELAYED_FROZEN):
		return typ, nil
	default:
		return 0, fmt.Errorf("ib credentials: invalid marketDataType %d", typ)
	}
}

func normalizeIBSubscriptions(
	cfg ibConfig, subs []Subscription,
) ([]ibSubscription, error) {
	normalized := make([]ibSubscription, 0, len(subs))
	for i, sub := range subs {
		external := strings.TrimSpace(sub.External)
		if external == "" {
			return nil, missingExternalSymbolError("ib", sub)
		}
		if sub.Base == 0 || sub.Quote == 0 {
			return nil, fmt.Errorf("ib subscription %q: asset key is incomplete", external)
		}
		contractCfg := cfg.ContractDefaults
		if cfg.Contracts != nil {
			if override, ok := cfg.Contracts[external]; ok {
				contractCfg = mergeIBContractConfig(contractCfg, override)
			}
		}
		if strings.TrimSpace(contractCfg.Symbol) == "" {
			contractCfg.Symbol = external
		}
		contractCfg.Symbol = strings.ToUpper(strings.TrimSpace(contractCfg.Symbol))
		currency := strings.TrimSpace(contractCfg.Currency)
		if currency == "" {
			return nil, fmt.Errorf(
				"ib subscription %q: IB contract currency is not configured",
				external,
			)
		}
		contractCfg.Currency = strings.ToUpper(currency)
		contract, err := ibContractFromConfig(contractCfg)
		if err != nil {
			return nil, err
		}
		sub.External = external
		normalized = append(normalized, ibSubscription{
			Subscription: sub,
			contract:     contract,
			reqID:        ibapi.TickerID(i + 1),
		})
	}
	return normalized, nil
}

func mergeIBContractConfig(base, override ibContractConfig) ibContractConfig {
	if override.Symbol != "" {
		base.Symbol = override.Symbol
	}
	if override.SecType != "" {
		base.SecType = override.SecType
	}
	if override.LastTradeDateOrContractMonth != "" {
		base.LastTradeDateOrContractMonth = override.LastTradeDateOrContractMonth
	}
	if override.Right != "" {
		base.Right = override.Right
	}
	if override.Multiplier != "" {
		base.Multiplier = override.Multiplier
	}
	if override.Exchange != "" {
		base.Exchange = override.Exchange
	}
	if override.PrimaryExchange != "" {
		base.PrimaryExchange = override.PrimaryExchange
	}
	if override.Currency != "" {
		base.Currency = override.Currency
	}
	if override.LocalSymbol != "" {
		base.LocalSymbol = override.LocalSymbol
	}
	if override.TradingClass != "" {
		base.TradingClass = override.TradingClass
	}
	if override.SecIDType != "" {
		base.SecIDType = override.SecIDType
	}
	if override.SecID != "" {
		base.SecID = override.SecID
	}
	if strings.TrimSpace(override.ConID) != "" {
		base.ConID = override.ConID
	}
	if strings.TrimSpace(override.Strike) != "" {
		base.Strike = override.Strike
	}
	if override.IncludeExpired {
		base.IncludeExpired = true
	}
	return base
}

func ibContractFromConfig(cfg ibContractConfig) (*ibapi.Contract, error) {
	conID, err := parseIBInt64(cfg.ConID, "conId")
	if err != nil {
		return nil, err
	}
	strike, err := parseIBFloat64(cfg.Strike, "strike")
	if err != nil {
		return nil, err
	}
	contract := ibapi.NewContract()
	contract.ConID = conID
	contract.Symbol = strings.TrimSpace(cfg.Symbol)
	contract.SecType = strings.TrimSpace(cfg.SecType)
	contract.LastTradeDateOrContractMonth = strings.TrimSpace(cfg.LastTradeDateOrContractMonth)
	contract.Right = strings.TrimSpace(cfg.Right)
	contract.Multiplier = strings.TrimSpace(cfg.Multiplier)
	contract.Exchange = strings.TrimSpace(cfg.Exchange)
	contract.PrimaryExchange = strings.TrimSpace(cfg.PrimaryExchange)
	contract.Currency = strings.TrimSpace(cfg.Currency)
	contract.LocalSymbol = strings.TrimSpace(cfg.LocalSymbol)
	contract.TradingClass = strings.TrimSpace(cfg.TradingClass)
	contract.SecIDType = strings.TrimSpace(cfg.SecIDType)
	contract.SecID = strings.TrimSpace(cfg.SecID)
	if strike != 0 {
		contract.Strike = strike
	}
	contract.IncludeExpired = cfg.IncludeExpired
	return contract, nil
}

func ibTickField(tickType ibapi.TickType) (string, bool) {
	switch tickType {
	case ibapi.BID, ibapi.DELAYED_BID:
		return "bid", true
	case ibapi.ASK, ibapi.DELAYED_ASK:
		return "ask", true
	case ibapi.LAST, ibapi.DELAYED_LAST, ibapi.MARK_PRICE:
		return "mark", true
	default:
		return "", false
	}
}

func ibFatalError(code int64) bool {
	switch code {
	case 502, 504, 1300:
		return true
	default:
		return false
	}
}

func ibInformationalError(code int64) bool {
	switch code {
	case 1102, 2104, 2106, 2107, 2108, 2158:
		return true
	default:
		return false
	}
}

func ibRecoverableConnectionError(code int64) bool {
	switch code {
	case 1100, 1101, 2103, 2105, 2110:
		return true
	default:
		return false
	}
}

func ibMarketDataUnavailableError(code int64) bool {
	switch code {
	case 354, 10167, 10168:
		return true
	default:
		return false
	}
}

func ibMarketDataTypeName(typ int64) string {
	switch typ {
	case int64(ibapi.REALTIME):
		return "realtime"
	case int64(ibapi.FROZEN):
		return "frozen"
	case int64(ibapi.DELAYED):
		return "delayed"
	case int64(ibapi.DELAYED_FROZEN):
		return "delayed frozen"
	default:
		return ""
	}
}

func ibConnectError(cfg ibConfig, err error) error {
	return fmt.Errorf(
		"ib: connect to TWS/Gateway at %s:%d failed: %w; ensure the API socket is enabled, trusted for localhost, and listening on the configured paper/live port",
		cfg.Host, cfg.Port, err,
	)
}
