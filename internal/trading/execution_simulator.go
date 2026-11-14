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
	"fmt"
	"math"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/marketdata"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/openpit/param"
)

// TestProvider executes paper orders locally through the normal report sink.
func TestProvider() fwtrading.Provider {
	return fwtrading.Provider{
		Type: "test", Title: "Execution Simulator", ExecutionPolicy: true,
		Build: func(ctx fwtrading.BuildContext) (fwtrading.Connector, error) {
			return newTestConnector(ctx, time.Now)
		},
	}
}

type testEntry struct {
	order      fwtrading.Order
	policy     domain.TestExecutionPolicyState
	base       domain.EngineAssetID
	quote      domain.EngineAssetID
	snapshot   fwtrading.Snapshot
	refusal    *fwtrading.TestExecutionRefusal
	ordinal    uint64
	ruleIndex  int
	state      string
	acceptedAt time.Time
	deadline   time.Time
	stopTimer  func() bool
}

type testConnector struct {
	mu              sync.Mutex
	connection      domain.TradingConnection
	quotes          fwtrading.QuoteSource
	store           fwtrading.TestExecutionStore
	now             func() time.Time
	schedule        func(context.Context, time.Time, func(time.Time)) (func() bool, error)
	ctx             context.Context
	handlers        sync.WaitGroup
	reportStatus    health.StatusReporter
	reportDiag      health.DiagnosticReporter
	book            map[string]*testEntry
	closed          bool
	cancel          context.CancelFunc
	done            chan struct{}
	wake            chan struct{}
	events          []fwtrading.Event
	currentRevision uint64
	ordinal         uint64
}

func newTestConnector(ctx fwtrading.BuildContext, now func() time.Time) (fwtrading.Connector, error) {
	if ctx.Quotes == nil || ctx.Store == nil {
		return nil, fmt.Errorf("test: Quotes and Store are required")
	}
	if ctx.Connection.Mode != domain.TradingModePaper {
		return nil, domain.NewValidationError("/mode", "enum", "test requires paper mode")
	}
	var credentials map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ctx.Connection.Credentials), &credentials); err != nil || credentials == nil {
		return nil, domain.NewValidationError("/credentials", "type", "test credentials must be an empty object")
	}
	for key := range credentials {
		key = strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
		return nil, domain.NewValidationError("/credentials/"+key, "forbidden", "test credentials must be empty")
	}
	return &testConnector{
		connection: ctx.Connection, quotes: ctx.Quotes, store: ctx.Store,
		now: now, schedule: ctx.Schedule, book: make(map[string]*testEntry), wake: make(chan struct{}, 1),
	}, nil
}

func (c *testConnector) CheckAccount(ctx context.Context) (fwtrading.AccountInfo, error) {
	if err := ctx.Err(); err != nil {
		return fwtrading.AccountInfo{}, err
	}
	id, err := domain.NewExternalID()
	if err != nil {
		return fwtrading.AccountInfo{}, err
	}
	return fwtrading.AccountInfo{Identity: "test:" + id.String()}, nil
}

func (c *testConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return fmt.Errorf("%w: %w", fwtrading.ErrUnsupported,
			domain.NewValidationError("/amountKind", "enum", "test requires quantity orders"))
	}
	var route map[string]json.RawMessage
	if err := json.Unmarshal([]byte(order.Route), &route); err != nil || route == nil {
		return domain.NewValidationError("/destination/route", "type", "test route must be an empty object")
	}
	if len(route) != 0 {
		return domain.NewValidationError("/destination/route", "forbidden", "test route must be empty")
	}
	return nil
}

func (c *testConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fwtrading.Ack{}, fmt.Errorf("test: connector closed: %w", domain.ErrConflict)
	}
	if entry, ok := c.book[order.ClientOrderID]; ok {
		if entry.order != order {
			return fwtrading.Ack{}, fmt.Errorf("test: client order id has different content: %w", domain.ErrConflict)
		}
		return testEntryResult(entry)
	}
	if err := c.PreCheck(ctx, order); err != nil {
		return fwtrading.Ack{}, err
	}
	if order.ClientOrderID == "" {
		return fwtrading.Ack{}, fmt.Errorf("test: client order id required: %w", domain.ErrInvalid)
	}
	policy, err := c.store.GetTestExecutionPolicy(ctx, c.connection.ExternalID)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	policy.Policy.Rules = slices.Clone(policy.Policy.Rules)
	indexes := []int{}
	for i, rule := range policy.Policy.Rules {
		if rule.Fills != nil {
			value := *rule.Fills
			policy.Policy.Rules[i].Fills = &value
		}
		if rule.Wait != nil {
			wait := *rule.Wait
			if wait.Seconds != nil {
				seconds := *wait.Seconds
				wait.Seconds = &seconds
			}
			policy.Policy.Rules[i].Wait = &wait
		}
		if len(indexes) == 0 && rule.Mask.Matches(order.BaseAsset, order.QuoteAsset) {
			indexes = append(indexes, i)
		}
	}
	entry := &testEntry{order: order, policy: policy, state: "open", snapshot: fwtrading.Snapshot{
		VenueOrderID: "test:" + c.connection.ExternalID.String() + ":" + order.ClientOrderID,
		Status:       fwtrading.VenueStatusOpen, FilledQuantity: "0",
	}}
	refuse := func(code, message string) {
		entry.refusal = &fwtrading.TestExecutionRefusal{Code: code,
			BaseAsset: order.BaseAsset, QuoteAsset: order.QuoteAsset,
			RuleIndexes: indexes, Message: message}
		entry.snapshot.Status = fwtrading.VenueStatusCancelled
		entry.snapshot.Reason = message
		entry.state = "terminal"
	}
	for _, asset := range []struct {
		code string
		id   *domain.EngineAssetID
	}{{order.BaseAsset, &entry.base}, {order.QuoteAsset, &entry.quote}} {
		value, found, err := c.store.GetAsset(ctx, asset.code)
		if err != nil {
			return fwtrading.Ack{}, err
		}
		if !found || value.EngineAssetID == 0 {
			refuse("test_quote_invalid", "Test asset identity is missing: "+asset.code)
		} else {
			*asset.id = value.EngineAssetID
		}
	}
	if len(indexes) == 0 {
		refuse("test_policy_no_match", "Unknown test symbol: "+order.BaseAsset+"/"+order.QuoteAsset)
	}
	quantity, err := param.NewQuantityFromString(order.Quantity)
	if err != nil && entry.refusal == nil {
		refuse("test_quantity_invalid", "Test quantity is invalid: "+err.Error())
	}
	counted := entry.refusal == nil && len(indexes) == 1 && policy.Policy.Rules[indexes[0]].Mode == "reject_even"
	if len(indexes) == 1 {
		entry.ruleIndex = indexes[0]
	}
	entry.acceptedAt = c.now()
	if entry.refusal == nil {
		rule := policy.Policy.Rules[entry.ruleIndex]
		if rule.Mode == "rest" && rule.Wait.Kind == "finite" {
			if c.ctx == nil || c.schedule == nil {
				return fwtrading.Ack{}, fmt.Errorf("test: finite rest requires Subscribe and the host scheduler: %w", domain.ErrConflict)
			}
			duration := time.Duration(*rule.Wait.Seconds) * time.Second
			entry.deadline = entry.acceptedAt.Add(duration)
			if !entry.deadline.After(entry.acceptedAt) || entry.deadline.Sub(entry.acceptedAt) != duration {
				refuse("test_deadline_overflow", "Test rest deadline overflow")
			}
		}
	}
	if counted {
		if c.currentRevision == policy.Revision {
			if c.ordinal == math.MaxUint64 {
				counted = false
				refuse("test_policy_ordinal_overflow", "Test policy order counter overflow")
			} else {
				entry.ordinal = c.ordinal + 1
			}
		} else {
			entry.ordinal = 1
		}
		if counted && entry.ordinal%2 == 0 {
			refuse("test_policy_even_rejected", "Test policy rejected every second order")
		}
	}
	// This check and acceptance share the book mutex with Lookup and Close.
	// After acceptance the saved outcome wins over a concurrent cancellation.
	if err := ctx.Err(); err != nil {
		return fwtrading.Ack{}, err
	}
	if c.ctx != nil && c.ctx.Err() != nil {
		return fwtrading.Ack{}, c.ctx.Err()
	}
	c.book[order.ClientOrderID] = entry
	if counted {
		c.currentRevision, c.ordinal = policy.Revision, entry.ordinal
	}
	if entry.refusal == nil {
		rule := policy.Policy.Rules[indexes[0]]
		if rule.Mode == "rest" {
			if rule.Wait.Kind == "finite" {
				if err := c.armRest(entry); err != nil {
					delete(c.book, order.ClientOrderID)
					return fwtrading.Ack{}, fmt.Errorf("test: schedule execution: %w: %w", err, fwtrading.ErrRejected)
				}
			}
			return testEntryResult(entry)
		}
		entry.state = "completing"
		price, code, message := c.price(entry, rule.PriceVariant)
		if code != "" {
			refuse(code, message)
		} else {
			fills := []fwtrading.Fill{{Quantity: order.Quantity,
				CumQuantity: order.Quantity, LeavesQuantity: "0", Final: true}}
			if rule.Mode == "split" {
				fills, err = testSplit(order.Quantity, quantity, *rule.Fills)
				if err != nil {
					refuse("test_split_unrepresentable", "Test split is unrepresentable: "+err.Error())
				}
			}
			if entry.refusal == nil {
				at := c.now()
				for i := range fills {
					fills[i].Price, fills[i].At = price, at
				}
				entry.snapshot.Status = fwtrading.VenueStatusFilled
				entry.snapshot.FilledQuantity = order.Quantity
				entry.snapshot.Fills = fills
				entry.state = "terminal"
			}
		}
	}
	if c.cancel != nil {
		c.events = append(c.events, fwtrading.Event{Kind: fwtrading.EventOrder,
			Ref: fwtrading.OrderRef{ClientOrderID: order.ClientOrderID, VenueOrderID: entry.snapshot.VenueOrderID}})
		c.notify()
	}
	return testEntryResult(entry)
}

func testSplit(raw string, quantity param.Quantity, n int) ([]fwtrading.Fill, error) {
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return nil, err
	}
	if !value.Equal(quantity.Decimal()) || !value.IsPositive() {
		return nil, fmt.Errorf("quantity does not exactly match its decimal input")
	}
	if n != 2 && n != 3 {
		return nil, fmt.Errorf("fill count must be 2 or 3")
	}
	exponent := value.Exponent()
	units := value.Coefficient()
	if exponent > 0 {
		units.Mul(units, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exponent)), nil))
		exponent = 0
	}
	divisor := big.NewInt(int64(n))
	if units.Cmp(divisor) < 0 {
		return nil, fmt.Errorf("quantity has fewer units than fills")
	}
	base, remainder := new(big.Int), new(big.Int)
	base.QuoRem(units, divisor, remainder)
	unit := decimal.New(1, exponent)
	sum, err := quantity.CheckedSub(quantity)
	if err != nil {
		return nil, err
	}
	fills := make([]fwtrading.Fill, n)
	for i := range fills {
		coefficient := new(big.Int).Set(base)
		if int64(i) < remainder.Int64() {
			coefficient.Add(coefficient, big.NewInt(1))
		}
		chunk, err := param.NewQuantityFromDecimal(decimal.NewFromBigInt(coefficient, exponent))
		if err != nil {
			return nil, err
		}
		chunkValue := chunk.Decimal()
		if !chunkValue.IsPositive() || !chunkValue.Mod(unit).IsZero() {
			return nil, fmt.Errorf("fill is not a positive multiple of the input unit")
		}
		sum, err = sum.CheckedAdd(chunk)
		if err != nil {
			return nil, err
		}
		leaves, err := quantity.CheckedSub(sum)
		if err != nil {
			return nil, err
		}
		if i < n-1 && !leaves.Decimal().IsPositive() {
			return nil, fmt.Errorf("partial fill has no positive leaves")
		}
		leavesText := leaves.Decimal().StringFixed(-exponent)
		if i == n-1 {
			leavesText = "0"
		}
		fills[i] = fwtrading.Fill{Quantity: chunkValue.StringFixed(-exponent),
			CumQuantity:    sum.Decimal().StringFixed(-exponent),
			LeavesQuantity: leavesText, Final: i == n-1}
	}
	if !sum.Decimal().Equal(quantity.Decimal()) {
		return nil, fmt.Errorf("fill sum does not equal quantity")
	}
	return fills, nil
}

func testEntryResult(entry *testEntry) (fwtrading.Ack, error) {
	if entry.refusal != nil {
		refusal := *entry.refusal
		refusal.RuleIndexes = slices.Clone(refusal.RuleIndexes)
		return fwtrading.Ack{}, &refusal
	}
	return fwtrading.Ack{VenueOrderID: entry.snapshot.VenueOrderID}, nil
}

func (c *testConnector) armRest(entry *testEntry) error {
	stop, err := c.schedule(c.ctx, entry.deadline, func(now time.Time) {
		c.mu.Lock()
		if c.closed || c.ctx.Err() != nil || c.book[entry.order.ClientOrderID] != entry || entry.state != "open" {
			c.mu.Unlock()
			return
		}
		c.handlers.Add(1)
		defer c.handlers.Done()
		entry.state = "completing"
		c.stopRest(entry)
		c.mu.Unlock()

		rule := entry.policy.Policy.Rules[entry.ruleIndex]
		price, code, message := c.price(entry, rule.PriceVariant)
		c.mu.Lock()
		if c.closed || c.ctx.Err() != nil || c.book[entry.order.ClientOrderID] != entry || entry.state != "completing" {
			c.mu.Unlock()
			return
		}
		entry.state = "terminal"
		if code != "" {
			entry.snapshot.Status = fwtrading.VenueStatusCancelled
			entry.snapshot.Reason = message
		} else {
			entry.snapshot.Status = fwtrading.VenueStatusFilled
			entry.snapshot.FilledQuantity = entry.order.Quantity
			entry.snapshot.Fills = []fwtrading.Fill{{Price: price, At: now,
				Quantity: entry.order.Quantity, CumQuantity: entry.order.Quantity, LeavesQuantity: "0", Final: true}}
		}
		report := c.reportDiag
		c.mu.Unlock()
		if code != "" && report != nil {
			report(health.Diagnostic{At: now.UTC(), Level: health.DiagWarn, Kind: health.DiagKindProvider,
				Code: code, Title: "Test order cancelled", Detail: message,
				Instrument: entry.order.BaseAsset + "/" + entry.order.QuoteAsset})
		}
		c.mu.Lock()
		if !c.closed && c.ctx.Err() == nil {
			c.events = append(c.events, fwtrading.Event{Kind: fwtrading.EventOrder,
				Ref: fwtrading.OrderRef{ClientOrderID: entry.order.ClientOrderID, VenueOrderID: entry.snapshot.VenueOrderID}})
			c.notify()
		}
		c.mu.Unlock()
	})
	if err != nil {
		return err
	}
	entry.stopTimer = stop
	return nil
}

func (c *testConnector) stopRest(entry *testEntry) {
	if entry.stopTimer != nil {
		entry.stopTimer()
		entry.stopTimer = nil
	}
}

func (c *testConnector) price(entry *testEntry, variant domain.TestPriceVariant) (string, string, string) {
	price := entry.order.LimitPrice
	if price == "" {
		snapshots := c.quotes.QuoteSnapshots()
		var candidates []marketdata.QuoteSnapshot
		inverse := false
		for _, snapshot := range snapshots {
			if snapshot.BaseAssetID == entry.base && snapshot.QuoteAssetID == entry.quote {
				candidates = append(candidates, snapshot)
			}
		}
		if len(candidates) == 0 {
			inverse = true
			for _, snapshot := range snapshots {
				if snapshot.BaseAssetID == entry.quote && snapshot.QuoteAssetID == entry.base {
					candidates = append(candidates, snapshot)
				}
			}
		}
		if len(candidates) == 0 {
			return "", "test_quote_missing", "Test quote is missing"
		}
		if len(candidates) > 1 {
			sources := make([]string, 0, len(candidates))
			for _, candidate := range candidates {
				sources = append(sources, candidate.Instance.String()+"/"+candidate.ExternalSymbol)
			}
			return "", "test_quote_ambiguous", "Test quote is ambiguous: " + strings.Join(sources, ", ")
		}
		selected := candidates[0]
		quote := selected.MarketDataQuote
		if quote.AsOf.IsZero() {
			return "", "test_quote_invalid", "Test quote has no source timestamp"
		}
		if marketdata.QuoteStale(quote.AsOf, c.now()) {
			return "", "test_quote_stale", "Test quote is stale"
		}
		if inverse {
			var ok bool
			quote, ok = marketdata.InverseQuote(quote, domain.MarketDataInstrument{
				BaseAssetID: selected.BaseAssetID, QuoteAssetID: selected.QuoteAssetID})
			if !ok {
				return "", "test_quote_invalid", "Test inverse quote is invalid"
			}
		}
		bid := entry.order.Side == domain.OrderSideSell
		if variant == "best" {
			bid = !bid
		}
		side := "ask"
		price = quote.Ask
		if bid {
			side, price = "bid", quote.Bid
		}
		if price == "" {
			return "", "test_quote_side_missing", fmt.Sprintf("Test quote for %s/%s has no %s: %s/%s",
				entry.order.BaseAsset, entry.order.QuoteAsset, side, quote.Instance, quote.ExternalSymbol)
		}
	}
	if _, err := param.NewPriceFromString(price); err != nil {
		return "", "test_quote_invalid", "Test price is invalid: " + err.Error()
	}
	return price, "", ""
}

func (c *testConnector) Lookup(ctx context.Context, ref fwtrading.OrderRef) (fwtrading.Snapshot, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return fwtrading.Snapshot{}, false, err
	}
	entry, found := c.book[ref.ClientOrderID]
	if !found {
		return fwtrading.Snapshot{}, false, fwtrading.ErrOrderGone
	}
	snapshot := entry.snapshot
	snapshot.Fills = slices.Clone(snapshot.Fills)
	for i, fill := range snapshot.Fills {
		if fill.Commission != nil {
			commission := *fill.Commission
			snapshot.Fills[i].Commission = &commission
		}
	}
	return snapshot, true, nil
}

func (c *testConnector) ReleaseOrder(clientOrderID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.book[clientOrderID]; entry != nil {
		c.stopRest(entry)
	}
	delete(c.book, clientOrderID)
}

func (c *testConnector) notify() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *testConnector) Subscribe(ctx context.Context) (<-chan fwtrading.Event, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.cancel != nil {
		return nil, fmt.Errorf("test: connector cannot subscribe: %w", domain.ErrConflict)
	}
	ctx, c.cancel = context.WithCancel(ctx)
	c.ctx = ctx
	if c.reportStatus != nil {
		c.reportStatus(true, "")
	}
	c.done = make(chan struct{})
	out := make(chan fwtrading.Event)
	c.events = append(c.events, fwtrading.Event{Kind: fwtrading.EventConnected})
	c.notify()
	go func() {
		defer func() {
			c.awaitInFlightSend()
			c.handlers.Wait()
			close(out)
			close(c.done)
		}()
		for {
			c.mu.Lock()
			var event fwtrading.Event
			hasEvent := len(c.events) != 0
			if hasEvent {
				event = c.events[0]
				c.events[0] = fwtrading.Event{}
				c.events = c.events[1:]
			}
			c.mu.Unlock()
			if hasEvent {
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			} else {
				select {
				case <-c.wake:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// awaitInFlightSend returns once any critical section that was running when it
// was called has finished. Send arms a rest timer, and so calls handlers.Add,
// inside c.mu; waiting for the lock first guarantees no Add can race the
// handlers.Wait that follows, because a later Send sees the cancelled context
// and returns before arming one. The section is empty on purpose: the
// acquisition is the barrier.
func (c *testConnector) awaitInFlightSend() {
	c.mu.Lock()
	defer c.mu.Unlock()
}

func (c *testConnector) SetStatusReporter(report health.StatusReporter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reportStatus = report
}

func (c *testConnector) SetDiagnosticReporter(report health.DiagnosticReporter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reportDiag = report
}

func (c *testConnector) Close() {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	done := c.done
	for _, entry := range c.book {
		c.stopRest(entry)
	}
	clear(c.book)
	c.events = nil
	c.mu.Unlock()
	c.handlers.Wait()
	if done != nil {
		<-done
	}
}
