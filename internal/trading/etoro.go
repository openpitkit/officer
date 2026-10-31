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
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/secret"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/etoro"
)

type etoroConnector struct {
	client       *etoro.Client
	demo         bool
	credentials  etoro.Credentials
	streamURL    string
	reportStatus health.StatusReporter
	reportDiag   health.DiagnosticReporter
	redact       *strings.Replacer
	lifecycle    streamLifecycle

	ordersMu sync.Mutex
	orderIDs map[string]string
	finished []string
	held     []string
	mapped   chan struct{}
}

var _ fwtrading.PreChecker = (*etoroConnector)(nil)
var _ fwtrading.Connector = (*etoroConnector)(nil)
var _ fwtrading.AccountChecker = (*etoroConnector)(nil)
var _ fwtrading.SymbolVerifier = (*etoroConnector)(nil)
var _ fwtrading.SymbolSearcher = (*etoroConnector)(nil)

// NewEtoroConnector selects the demo account for paper mode and the real
// account for live mode. Unknown modes are refused before contacting eToro.
func NewEtoroConnector(connection domain.TradingConnection) (fwtrading.Connector, error) {
	c, err := newEtoroConnector(connection, etoro.RESTURL)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func newEtoroConnector(connection domain.TradingConnection, restURL string) (*etoroConnector, error) {
	var demo bool
	switch connection.Mode {
	case domain.TradingModePaper:
		demo = true
	case domain.TradingModeLive:
		demo = false
	default:
		return nil, errors.New("etoro: unsupported trading mode")
	}
	credentials, err := etoro.ParseCredentials(connection.Credentials)
	if err != nil {
		return nil, domain.WithValidationPointer("/credentials"+domain.ValidationPointer(err), err)
	}
	_, redact, err := secret.CredentialRedactor(connection.Credentials)
	if err != nil {
		return nil, fmt.Errorf("etoro: %w", err)
	}
	return &etoroConnector{
		client: etoro.NewClient(restURL, credentials, &http.Client{Timeout: 10 * time.Second}, redact),
		demo:   demo, credentials: credentials, streamURL: etoro.StreamURL, redact: redact,
		orderIDs: make(map[string]string), mapped: make(chan struct{}, 1),
	}, nil
}

func parseEtoroRoute(raw string) (string, error) {
	var route map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &route); err != nil || route == nil {
		return "", fmt.Errorf("%w: invalid eToro route JSON", fwtrading.ErrRejected)
	}
	for field := range route {
		if field != "timeInForce" {
			return "", fmt.Errorf("%w: unknown eToro route field", fwtrading.ErrRejected)
		}
	}
	if len(route) == 0 {
		return "mkt", nil
	}
	var tif string
	if err := json.Unmarshal(route["timeInForce"], &tif); err != nil || tif != "ioc" {
		return "", fmt.Errorf("%w: unsupported eToro timeInForce", fwtrading.ErrRejected)
	}
	return "limitIOC", nil
}

func etoroRequestID(coid string) (string, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(coid)
	if err != nil || len(raw) != 16 || len(coid) != domain.ExternalIDStringLen {
		return "", fmt.Errorf("%w: eToro client order id must encode exactly 16 bytes", fwtrading.ErrRejected)
	}
	// eToro demo probe P3: non-RFC4122 UUID bytes are accepted as x-request-id.
	id, err := uuid.FromBytes(raw)
	if err != nil {
		return "", fmt.Errorf("%w: invalid eToro client order id", fwtrading.ErrRejected)
	}
	return id.String(), nil
}

type etoroOrderInput struct {
	instrumentID json.Number
	quantity     decimal.Decimal
	orderType    string
	limitPrice   json.Number
}

func validateEtoroOrder(order fwtrading.Order) (etoroOrderInput, error) {
	var input etoroOrderInput
	var err error
	input.orderType, err = parseEtoroRoute(order.Route)
	if err != nil {
		return input, err
	}
	if order.AmountKind != domain.OrderAmountKindQuantity {
		return input, fmt.Errorf("%w: eToro requires quantity orders", fwtrading.ErrUnsupported)
	}
	input.instrumentID, err = etoroInstrumentID(order.Symbol)
	if err != nil {
		return input, err
	}
	input.quantity, err = positiveDecimal(order.Quantity, "quantity")
	if err != nil {
		return input, err
	}
	if order.Side != domain.OrderSideBuy && order.Side != domain.OrderSideSell {
		return input, fmt.Errorf("%w: unsupported eToro order side", fwtrading.ErrUnsupported)
	}
	if (input.orderType == "limitIOC") != (order.LimitPrice != "") ||
		(order.Side == domain.OrderSideSell && order.LimitPrice != "") {
		return input, fmt.Errorf("%w: eToro limit orders require an IOC buy with a limit price", fwtrading.ErrUnsupported)
	}
	if order.LimitPrice != "" {
		price, err := positiveDecimal(order.LimitPrice, "limit price")
		if err != nil {
			return input, err
		}
		input.limitPrice = json.Number(price.String())
	}
	return input, nil
}

func etoroInstrumentID(external string) (json.Number, error) {
	if external == "" || external[0] < '1' || external[0] > '9' {
		return "", fmt.Errorf("%w: eToro symbol must be a positive integer instrument id", fwtrading.ErrRejected)
	}
	for _, digit := range external {
		if digit < '0' || digit > '9' {
			return "", fmt.Errorf("%w: eToro symbol must be a positive integer instrument id", fwtrading.ErrRejected)
		}
	}
	return json.Number(external), nil
}

func etoroDecimal(raw json.Number, field string) (decimal.Decimal, error) {
	value, err := decimal.NewFromString(raw.String())
	if err != nil {
		return decimal.Decimal{}, fmt.Errorf("etoro: missing or invalid %s: %w", field, domain.ErrUpstream)
	}
	return value, nil
}

func etoroInteger(raw json.Number, field string, positive bool) (decimal.Decimal, error) {
	value, err := etoroDecimal(raw, field)
	if err != nil {
		return decimal.Zero, err
	}
	if !value.IsInteger() || value.IsNegative() || (positive && value.IsZero()) {
		return decimal.Zero, fmt.Errorf("etoro: invalid %s: %w", field, domain.ErrUpstream)
	}
	return value, nil
}

type etoroEligibility struct {
	InstrumentID              json.Number `json:"instrumentId"`
	AllowOpenPosition         *bool       `json:"allowOpenPosition"`
	AllowClosePosition        *bool       `json:"allowClosePosition"`
	AllowPartialClosePosition *bool       `json:"allowPartialClosePosition"`
	AllowedOrderQuantityType  string      `json:"allowedOrderQuantityType"`
	UnitsQuantityType         string      `json:"unitsQuantityType"`
	MaxUnitsPerOrder          json.Number `json:"maxUnitsPerOrder"`
	LeverageConfigs           *[]struct {
		SettlementType string        `json:"settlementType"`
		Direction      string        `json:"direction"`
		LeverageValues []json.Number `json:"leverageValues"`
		IsPotential    *bool         `json:"isPotential"`
	} `json:"leverageConfigs"`
}

func (c *etoroConnector) eligibility(ctx context.Context, id json.Number) (etoroEligibility, error) {
	eligibilities, err := c.eligibilities(ctx, []json.Number{id})
	if err != nil {
		return etoroEligibility{}, err
	}
	eligibility, exists := eligibilities[id.String()]
	if !exists {
		return etoroEligibility{}, fmt.Errorf("%w: eToro instrument not found", fwtrading.ErrRejected)
	}
	return eligibility, nil
}

func (c *etoroConnector) eligibilities(ctx context.Context, ids []json.Number) (map[string]etoroEligibility, error) {
	var response struct {
		Eligibilities         []etoroEligibility `json:"eligibilities"`
		NotFoundInstrumentIDs []json.Number      `json:"notFoundInstrumentIds"`
	}
	_, err := c.client.Request(ctx, http.MethodPost,
		etoro.TradingPath(c.demo, "/api/v2/trading/info", "/eligibility"),
		struct {
			InstrumentIDs []json.Number `json:"instrumentIds"`
		}{ids}, &response)
	if err != nil {
		return nil, err
	}
	missingIDs := make(map[string]bool, len(response.NotFoundInstrumentIDs))
	for _, missing := range response.NotFoundInstrumentIDs {
		value, err := etoroInteger(missing, "notFoundInstrumentIds", true)
		if err != nil {
			return nil, err
		}
		missingIDs[value.String()] = true
	}
	eligibilities := make(map[string]etoroEligibility, len(response.Eligibilities))
	for _, eligibility := range response.Eligibilities {
		value, err := etoroInteger(eligibility.InstrumentID, "eligibility instrumentId", true)
		if err != nil {
			return nil, err
		}
		eligibilities[value.String()] = eligibility
	}
	for _, id := range ids {
		if missingIDs[id.String()] {
			// eToro demo probe P10: the eligibility service may not know every market-data catalogue instrument; such a match is shown untradable rather than failing the search.
			delete(eligibilities, id.String())
			continue
		}
		if _, exists := eligibilities[id.String()]; !exists {
			return nil, fmt.Errorf("%w: eToro instrument eligibility absent", fwtrading.ErrRejected)
		}
	}
	return eligibilities, nil
}

func checkEtoroBuyAvailability(eligibility etoroEligibility) error {
	if eligibility.AllowOpenPosition == nil {
		return fmt.Errorf("etoro: missing allowOpenPosition: %w", domain.ErrUpstream)
	}
	if !*eligibility.AllowOpenPosition {
		return fmt.Errorf("%w: eToro opening positions is not allowed", fwtrading.ErrRejected)
	}
	if eligibility.LeverageConfigs == nil {
		return fmt.Errorf("etoro: missing leverageConfigs: %w", domain.ErrUpstream)
	}
	allowed := false
	for _, config := range *eligibility.LeverageConfigs {
		switch config.SettlementType {
		// The demo eligibility service spells futures "reaL_FUTURES"; the
		// published schema spells it "realFutures".
		case "real", "cfd", "realFutures", "reaL_FUTURES", "marginTrade":
		default:
			return fmt.Errorf("etoro: unknown leverage settlementType: %w", domain.ErrUpstream)
		}
		if config.Direction != "long" && config.Direction != "short" {
			return fmt.Errorf("etoro: unknown leverage direction: %w", domain.ErrUpstream)
		}
		if config.IsPotential == nil || config.LeverageValues == nil {
			return fmt.Errorf("etoro: missing leverage configuration fields: %w", domain.ErrUpstream)
		}
		for _, leverage := range config.LeverageValues {
			value, err := etoroInteger(leverage, "leverageValues", true)
			if err != nil {
				return err
			}
			if value.Equal(decimal.NewFromInt(1)) && config.SettlementType == "real" &&
				config.Direction == "long" && !*config.IsPotential {
				allowed = true
			}
		}
	}
	if !allowed {
		return fmt.Errorf("%w: eToro requires available real long leverage 1", fwtrading.ErrUnsupported)
	}
	switch eligibility.AllowedOrderQuantityType {
	case "unitsOnly", "all":
	case "amountOnly":
		return fmt.Errorf("%w: eToro instrument allows amount orders only", fwtrading.ErrUnsupported)
	default:
		return fmt.Errorf("etoro: unknown allowedOrderQuantityType: %w", domain.ErrUpstream)
	}
	return nil
}

func checkEtoroBuy(eligibility etoroEligibility, quantity decimal.Decimal) error {
	if err := checkEtoroBuyAvailability(eligibility); err != nil {
		return err
	}
	switch eligibility.UnitsQuantityType {
	case "fractional":
	case "whole":
		if !quantity.IsInteger() {
			return fmt.Errorf("%w: eToro instrument requires whole units", fwtrading.ErrRejected)
		}
	default:
		return fmt.Errorf("etoro: unknown unitsQuantityType: %w", domain.ErrUpstream)
	}
	max, err := etoroDecimal(eligibility.MaxUnitsPerOrder, "maxUnitsPerOrder")
	if err != nil {
		return err
	}
	if !max.IsPositive() {
		return fmt.Errorf("etoro: invalid maxUnitsPerOrder: %w", domain.ErrUpstream)
	}
	if quantity.GreaterThan(max) {
		return fmt.Errorf("%w: quantity exceeds eToro maxUnitsPerOrder", fwtrading.ErrRejected)
	}
	return nil
}

func etoroTradable(eligibility etoroEligibility) (bool, error) {
	err := checkEtoroBuyAvailability(eligibility)
	if errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) {
		return false, nil
	}
	return err == nil, err
}

// CheckAccount identifies the selected account independently of the key pair.
// eToro reports no account status; Status is empty.
func (c *etoroConnector) CheckAccount(ctx context.Context) (fwtrading.AccountInfo, error) {
	var response struct {
		DemoCID json.Number `json:"demoCid"`
		RealCID json.Number `json:"realCid"`
	}
	if _, err := c.client.Request(ctx, http.MethodGet, "/api/v1/me", nil, &response); err != nil {
		return fwtrading.AccountInfo{}, err
	}
	cid, field := response.RealCID, "realCid"
	if c.demo {
		cid, field = response.DemoCID, "demoCid"
	}
	identity, err := etoroInteger(cid, field, true)
	if err != nil {
		return fwtrading.AccountInfo{}, err
	}
	return fwtrading.AccountInfo{Identity: identity.String()}, nil
}

type etoroInstrument struct {
	InstrumentID json.Number `json:"instrumentId"`
	DisplayName  string      `json:"displayName"`
	Type         string      `json:"type"`
	Symbol       string      `json:"symbol"`
}

func (c *etoroConnector) instruments(ctx context.Context, path string) ([]etoroInstrument, error) {
	// eToro demo probe P11: GET /api/v2/market-data/instruments is assumed to answer under "results" (marketDataResponse); the spec's search description says "instruments". A missing key fails loudly.
	var response struct {
		Results []etoroInstrument `json:"results"`
	}
	if _, err := c.client.Request(ctx, http.MethodGet, path, nil, &response); err != nil {
		return nil, err
	}
	if response.Results == nil {
		return nil, fmt.Errorf("etoro: missing instrument results: %w", domain.ErrUpstream)
	}
	return response.Results, nil
}

// VerifySymbol checks catalogue presence of a decimal instrument id, then the
// account's available real, long, leverage-1 opening configuration and an
// allowed quantity type. An external that is not an instrument id, or the
// catalogue's documented 404, is not found.
func (c *etoroConnector) VerifySymbol(ctx context.Context, external string) (fwtrading.SymbolVerification, error) {
	id, err := etoroInstrumentID(external)
	if err != nil {
		return fwtrading.SymbolVerification{}, nil
	}
	instruments, err := c.instruments(ctx, "/api/v2/market-data/instruments?instrumentsIds="+id.String())
	if err != nil {
		var apiErr *etoro.APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
			return fwtrading.SymbolVerification{}, nil
		}
		return fwtrading.SymbolVerification{}, err
	}
	for _, instrument := range instruments {
		value, err := etoroInteger(instrument.InstrumentID, "catalogue instrumentId", true)
		if err != nil {
			return fwtrading.SymbolVerification{}, err
		}
		if value.String() != id.String() {
			continue
		}
		eligibilities, err := c.eligibilities(ctx, []json.Number{id})
		if err != nil {
			return fwtrading.SymbolVerification{}, err
		}
		tradable := false
		if eligibility, exists := eligibilities[id.String()]; exists {
			tradable, err = etoroTradable(eligibility)
			if err != nil {
				return fwtrading.SymbolVerification{}, err
			}
		}
		return fwtrading.SymbolVerification{
			Exists: true, Tradable: tradable,
			Details: instrument.Type + " " + instrument.Symbol + " " + instrument.DisplayName,
		}, nil
	}
	return fwtrading.SymbolVerification{}, nil
}

// SearchSymbols preserves venue order and checks all matches in one eligibility
// request. The catalogue supplies neither exchange names nor base/quote assets.
func (c *etoroConnector) SearchSymbols(ctx context.Context, query string) ([]fwtrading.SymbolMatch, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []fwtrading.SymbolMatch{}, nil
	}
	if utf8.RuneCountInString(query) > 100 {
		return nil, fmt.Errorf("eToro search query exceeds 100 characters: %w", domain.ErrInvalid)
	}
	instruments, err := c.instruments(ctx, "/api/v2/market-data/instruments/search?"+url.Values{
		"query": {query}, "limit": {"50"},
	}.Encode())
	if err != nil {
		return nil, err
	}
	matches := make([]fwtrading.SymbolMatch, 0, len(instruments))
	if len(instruments) == 0 {
		return matches, nil
	}
	if len(instruments) > 50 {
		return nil, fmt.Errorf("etoro: search returned more than 50 instruments: %w", domain.ErrUpstream)
	}
	ids := make([]json.Number, 0, len(instruments))
	for _, instrument := range instruments {
		id, err := etoroInteger(instrument.InstrumentID, "catalogue instrumentId", true)
		if err != nil {
			return nil, err
		}
		ids = append(ids, json.Number(id.String()))
	}
	eligibilities, err := c.eligibilities(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i, instrument := range instruments {
		tradable := false
		if eligibility, exists := eligibilities[ids[i].String()]; exists {
			tradable, err = etoroTradable(eligibility)
			if err != nil {
				return nil, err
			}
		}
		matches = append(matches, fwtrading.SymbolMatch{
			Symbol: ids[i].String(), Name: instrument.Symbol + " - " + instrument.DisplayName,
			Class: instrument.Type, Tradable: tradable,
		})
	}
	return matches, nil
}

type etoroPosition struct {
	PositionID       json.Number `json:"positionID"`
	InstrumentID     json.Number `json:"instrumentID"`
	IsBuy            *bool       `json:"isBuy"`
	SettlementTypeID json.Number `json:"settlementTypeID"`
	Leverage         json.Number `json:"leverage"`
	MirrorID         json.Number `json:"mirrorID"`
	ParentPositionID json.Number `json:"parentPositionID"`
	Units            json.Number `json:"units"`
	OpenDateTime     time.Time   `json:"openDateTime"`
}

func (c *etoroConnector) sellPosition(ctx context.Context, input etoroOrderInput) (etoroPosition, error) {
	var portfolio struct {
		ClientPortfolio *struct {
			Positions *[]etoroPosition `json:"positions"`
		} `json:"clientPortfolio"`
	}
	_, err := c.client.Request(ctx, http.MethodGet,
		etoro.TradingPath(c.demo, "/api/v1/trading/info", "/portfolio"), nil, &portfolio)
	if err != nil {
		return etoroPosition{}, err
	}
	if portfolio.ClientPortfolio == nil || portfolio.ClientPortfolio.Positions == nil {
		return etoroPosition{}, fmt.Errorf("etoro: missing portfolio positions: %w", domain.ErrUpstream)
	}
	var chosen etoroPosition
	var chosenID decimal.Decimal
	for _, position := range *portfolio.ClientPortfolio.Positions {
		instrument, err := etoroInteger(position.InstrumentID, "position instrumentID", true)
		if err != nil {
			return etoroPosition{}, err
		}
		if instrument.String() != input.instrumentID.String() {
			continue
		}
		if position.IsBuy == nil {
			return etoroPosition{}, fmt.Errorf("etoro: missing position isBuy: %w", domain.ErrUpstream)
		}
		settlement, err := etoroInteger(position.SettlementTypeID, "settlementTypeID", false)
		if err != nil {
			return etoroPosition{}, err
		}
		switch settlement.String() {
		case "0", "1", "2", "3", "4":
		default:
			return etoroPosition{}, fmt.Errorf("etoro: unknown settlementTypeID: %w", domain.ErrUpstream)
		}
		leverage, err := etoroDecimal(position.Leverage, "position leverage")
		if err != nil {
			return etoroPosition{}, err
		}
		mirror, err := etoroInteger(position.MirrorID, "mirrorID", false)
		if err != nil {
			return etoroPosition{}, err
		}
		parent, err := etoroInteger(position.ParentPositionID, "parentPositionID", false)
		if err != nil {
			return etoroPosition{}, err
		}
		units, err := etoroDecimal(position.Units, "position units")
		if err != nil {
			return etoroPosition{}, err
		}
		if !*position.IsBuy || !settlement.Equal(decimal.NewFromInt(1)) || !leverage.Equal(decimal.NewFromInt(1)) ||
			!mirror.IsZero() || !parent.IsZero() || units.LessThan(input.quantity) {
			continue
		}
		id, err := etoroInteger(position.PositionID, "positionID", true)
		if err != nil {
			return etoroPosition{}, err
		}
		if position.OpenDateTime.IsZero() {
			return etoroPosition{}, fmt.Errorf("etoro: missing position openDateTime: %w", domain.ErrUpstream)
		}
		position.PositionID = json.Number(id.String())
		if chosen.PositionID == "" || position.OpenDateTime.Before(chosen.OpenDateTime) ||
			(position.OpenDateTime.Equal(chosen.OpenDateTime) && id.LessThan(chosenID)) {
			chosen, chosenID = position, id
		}
	}
	if chosen.PositionID == "" {
		return etoroPosition{}, fmt.Errorf("%w: no single eToro position covers the sell", fwtrading.ErrUnsupported)
	}
	return chosen, nil
}

func (c *etoroConnector) checkOrder(ctx context.Context, order fwtrading.Order) (etoroOrderInput, etoroPosition, error) {
	input, err := validateEtoroOrder(order)
	if err != nil {
		return input, etoroPosition{}, err
	}
	eligibility, err := c.eligibility(ctx, input.instrumentID)
	if err != nil {
		return input, etoroPosition{}, err
	}
	if order.Side == domain.OrderSideBuy {
		return input, etoroPosition{}, checkEtoroBuy(eligibility, input.quantity)
	}
	if eligibility.AllowClosePosition == nil {
		return input, etoroPosition{}, fmt.Errorf("etoro: missing allowClosePosition: %w", domain.ErrUpstream)
	}
	if !*eligibility.AllowClosePosition {
		return input, etoroPosition{}, fmt.Errorf("%w: eToro closing positions is not allowed", fwtrading.ErrRejected)
	}
	position, err := c.sellPosition(ctx, input)
	if err != nil {
		return input, position, err
	}
	units, err := etoroDecimal(position.Units, "position units")
	if err != nil {
		return input, position, err
	}
	if input.quantity.LessThan(units) {
		if eligibility.AllowPartialClosePosition == nil {
			return input, position, fmt.Errorf("etoro: missing allowPartialClosePosition: %w", domain.ErrUpstream)
		}
		if !*eligibility.AllowPartialClosePosition {
			return input, position, fmt.Errorf("%w: eToro partial closing is not allowed", fwtrading.ErrUnsupported)
		}
	}
	return input, position, nil
}

func (c *etoroConnector) PreCheck(ctx context.Context, order fwtrading.Order) error {
	_, _, err := c.checkOrder(ctx, order)
	return err
}

func (c *etoroConnector) Send(ctx context.Context, order fwtrading.Order) (fwtrading.Ack, error) {
	requestID, err := etoroRequestID(order.ClientOrderID)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	// The runtime calls PreCheck immediately before Send on the send path;
	// eligibility is checked there. A sell still resolves its position again.
	input, err := validateEtoroOrder(order)
	if err != nil {
		return fwtrading.Ack{}, err
	}
	var position etoroPosition
	if order.Side == domain.OrderSideSell {
		position, err = c.sellPosition(ctx, input)
		if err != nil {
			if errors.Is(err, fwtrading.ErrRejected) || errors.Is(err, fwtrading.ErrUnsupported) {
				return fwtrading.Ack{}, err
			}
			return fwtrading.Ack{}, fmt.Errorf("%w: no order request was made: %v", fwtrading.ErrRejected, err)
		}
	}
	var status int
	var id json.Number
	var expected int
	if order.Side == domain.OrderSideBuy {
		body := struct {
			Action         string      `json:"action"`
			Transaction    string      `json:"transaction"`
			InstrumentID   json.Number `json:"instrumentId"`
			SettlementType string      `json:"settlementType"`
			Leverage       json.Number `json:"leverage"`
			OrderType      string      `json:"orderType"`
			Units          json.Number `json:"units"`
			LimitRate      json.Number `json:"limitRate,omitempty"`
		}{"open", "buy", input.instrumentID, "real", "1", input.orderType, json.Number(input.quantity.String()), input.limitPrice}
		var response struct {
			OrderID json.Number `json:"orderId"`
		}
		status, err = c.client.RequestOrder(ctx, http.MethodPost,
			etoro.TradingPath(c.demo, "/api/v3/trading/execution", "/orders"), requestID, body, &response)
		id, expected = response.OrderID, http.StatusAccepted
	} else {
		instrumentKey := "InstrumentId"
		if c.demo {
			instrumentKey = "InstrumentID"
		}
		units, unitsErr := etoroDecimal(position.Units, "position units")
		if unitsErr != nil {
			return fwtrading.Ack{}, fmt.Errorf("%w: no order request was made: %v", fwtrading.ErrRejected, unitsErr)
		}
		body := map[string]json.Number{instrumentKey: input.instrumentID}
		// The venue refuses UnitsToDeduct equal to the position (remaining
		// amount below $1); an omitted UnitsToDeduct closes the whole position.
		if input.quantity.LessThan(units) {
			body["UnitsToDeduct"] = json.Number(input.quantity.String())
		}
		var response struct {
			OrderForClose *struct {
				OrderID json.Number `json:"orderID"`
			} `json:"orderForClose"`
		}
		status, err = c.client.RequestOrder(ctx, http.MethodPost,
			etoro.TradingPath(c.demo, "/api/v1/trading/execution", "/market-close-orders/positions/"+position.PositionID.String()),
			requestID, body, &response)
		if response.OrderForClose != nil {
			id = response.OrderForClose.OrderID
		}
		expected = http.StatusOK
	}
	if err != nil {
		var apiErr *etoro.APIError
		if errors.As(err, &apiErr) && apiErr.Status >= 400 && apiErr.Status < 500 && apiErr.Status != 408 {
			return fwtrading.Ack{}, fmt.Errorf("%w: %v", fwtrading.ErrRejected, err)
		}
		return fwtrading.Ack{}, fmt.Errorf("etoro: send outcome unknown: %w", err)
	}
	orderID, idErr := etoroInteger(id, "order id", true)
	if status != expected || idErr != nil {
		return fwtrading.Ack{}, errors.New("etoro: send outcome unknown: expected acknowledgement with an order id")
	}
	c.mapOrder(orderID.String(), order.ClientOrderID)
	return fwtrading.Ack{VenueOrderID: orderID.String()}, nil
}

func etoroVenueStatus(id json.Number) (fwtrading.VenueStatus, bool) {
	switch id.String() {
	case "1", "2", "5", "6", "11", "12":
		return fwtrading.VenueStatusOpen, true
	case "3":
		return fwtrading.VenueStatusFilled, true
	case "4", "7", "8", "9", "10":
		return fwtrading.VenueStatusCancelled, true
	default:
		return "", false
	}
}

func etoroLookupUnresolved() health.Diagnostic {
	return venueLookupUnparsable("An eToro order lookup did not resolve the order (probe P2/P6).")
}

func (c *etoroConnector) lookupUnparsable(detail string) error {
	c.reportDiag(venueLookupUnparsable(detail))
	return fmt.Errorf("etoro: %s: %w", detail, domain.ErrUpstream)
}

func etoroDecodeFailure(err error) bool {
	var netErr net.Error
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) {
		return false
	}
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	return errors.As(err, &syntaxErr) || errors.As(err, &typeErr) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func (c *etoroConnector) lookupUnresolved() error {
	c.reportDiag(etoroLookupUnresolved())
	return fmt.Errorf("etoro: order not resolved (probe P2/P6): %w", domain.ErrUpstream)
}

type etoroOpeningExecution struct {
	PositionID  json.Number `json:"positionId"`
	OpeningData *struct {
		Units         json.Number `json:"units"`
		AvgPrice      json.Number `json:"avgPrice"`
		ExecutionTime time.Time   `json:"executionTime"`
		Fees          json.Number `json:"fees"`
		Taxes         json.Number `json:"taxes"`
	} `json:"openingData"`
}

type etoroExecution struct {
	positionID decimal.Decimal
	quantity   decimal.Decimal
	price      decimal.Decimal
	fee        decimal.Decimal
	at         time.Time
}

func etoroOpenExecutions(positions []etoroOpeningExecution) ([]etoroExecution, error) {
	executions := make([]etoroExecution, 0, len(positions))
	for _, position := range positions {
		if position.OpeningData == nil || position.OpeningData.ExecutionTime.IsZero() {
			return nil, fmt.Errorf("etoro: missing opening execution data: %w", domain.ErrUpstream)
		}
		data := position.OpeningData
		id, err := etoroInteger(position.PositionID, "execution positionId", true)
		if err != nil {
			return nil, err
		}
		quantity, err := etoroDecimal(data.Units, "opening units")
		if err != nil {
			return nil, err
		}
		price, err := etoroDecimal(data.AvgPrice, "opening avgPrice")
		if err != nil {
			return nil, err
		}
		fees, err := etoroDecimal(data.Fees, "opening fees")
		if err != nil {
			return nil, err
		}
		taxes, err := etoroDecimal(data.Taxes, "opening taxes")
		if err != nil {
			return nil, err
		}
		// eToro demo probe P5: fees are final and in account currency; taxes fail.
		if !taxes.IsZero() {
			return nil, fmt.Errorf("etoro: non-zero opening taxes cannot be accounted for (probe P5): %w", domain.ErrUpstream)
		}
		if !quantity.IsPositive() || !price.IsPositive() {
			return nil, fmt.Errorf("etoro: opening units and avgPrice must be positive: %w", domain.ErrUpstream)
		}
		executions = append(executions, etoroExecution{id, quantity, price, fees, data.ExecutionTime})
	}
	return executions, nil
}

// etoroOrderState is one order as the venue reports it, before it becomes a
// Snapshot. currency is the commission currency; a close carries none (probe P7).
type etoroOrderState struct {
	orderID    decimal.Decimal
	statusID   json.Number
	status     fwtrading.VenueStatus
	reason     string
	requested  decimal.Decimal
	executions []etoroExecution
	currency   string
}

// Lookup routes by the side of Officer's order: a buy opens a position and
// resolves through orders:lookup, a sell closes one through close-orders.
func (c *etoroConnector) Lookup(ctx context.Context, ref fwtrading.OrderRef) (fwtrading.Snapshot, bool, error) {
	if c.reportDiag == nil {
		return fwtrading.Snapshot{}, true, errors.New("etoro: lookup requires a diagnostic reporter")
	}
	var state etoroOrderState
	var err error
	switch ref.Side {
	case domain.OrderSideBuy:
		state, err = c.lookupOpen(ctx, ref)
	case domain.OrderSideSell:
		state, err = c.lookupClose(ctx, ref)
	default:
		return fwtrading.Snapshot{}, true, fmt.Errorf("etoro: lookup requires the order side, got %q: %w", ref.Side, domain.ErrInvalid)
	}
	if err != nil {
		return fwtrading.Snapshot{}, true, err
	}
	return c.orderSnapshot(ref, state)
}

func (c *etoroConnector) lookupOpen(ctx context.Context, ref fwtrading.OrderRef) (etoroOrderState, error) {
	query := url.Values{}
	if ref.VenueOrderID != "" {
		query.Set("orderId", ref.VenueOrderID)
	} else {
		id, err := etoroRequestID(ref.ClientOrderID)
		if err != nil {
			return etoroOrderState{}, err
		}
		query.Set("referenceId", id)
	}
	var order struct {
		OrderID        json.Number `json:"orderId"`
		Action         string      `json:"action"`
		RequestedUnits json.Number `json:"requestedUnits"`
		Status         *struct {
			ID           json.Number `json:"id"`
			ErrorMessage string      `json:"errorMessage"`
		} `json:"status"`
		PositionExecutions *[]etoroOpeningExecution `json:"positionExecutions"`
	}
	_, err := c.client.Request(ctx, http.MethodGet,
		etoro.TradingPath(c.demo, "/api/v2/trading/info", "/orders:lookup")+"?"+query.Encode(), nil, &order)
	var apiErr *etoro.APIError
	// eToro demo probe P6: unknown not-found shapes cannot prove not sent.
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return etoroOrderState{}, c.lookupUnresolved()
	}
	if err != nil {
		if etoroDecodeFailure(err) {
			return etoroOrderState{}, fmt.Errorf("%w: %w", c.lookupUnparsable("An eToro order lookup could not be read."), err)
		}
		return etoroOrderState{}, err
	}
	if order.OrderID == "" || order.OrderID == "0" {
		return etoroOrderState{}, c.lookupUnresolved()
	}
	orderID, err := etoroInteger(order.OrderID, "lookup orderId", true)
	if err != nil {
		return etoroOrderState{}, c.lookupUnparsable("An eToro order lookup has an invalid orderId.")
	}
	if order.Status == nil {
		return etoroOrderState{}, c.lookupUnparsable("An eToro order lookup is missing its status.")
	}
	status, known := etoroVenueStatus(order.Status.ID)
	if !known {
		return etoroOrderState{}, c.lookupUnparsable("An eToro order lookup returned an unknown status.")
	}
	requested, err := etoroDecimal(order.RequestedUnits, "requestedUnits")
	if err != nil || !requested.IsPositive() {
		return etoroOrderState{}, c.lookupUnparsable("An eToro order lookup has missing or invalid requestedUnits.")
	}
	if order.Action != "open" {
		return etoroOrderState{}, c.lookupUnparsable("An eToro order lookup for a buy returned a non-open action.")
	}
	if order.PositionExecutions == nil {
		return etoroOrderState{}, c.lookupUnparsable("An eToro order lookup is missing positionExecutions.")
	}
	executions, err := etoroOpenExecutions(*order.PositionExecutions)
	if err != nil {
		c.reportDiag(venueLookupUnparsable("An eToro order lookup returned unreadable execution records."))
		return etoroOrderState{}, err
	}
	if len(executions) == 0 && (order.Status.ID == "3" || order.Status.ID == "5" ||
		order.Status.ID == "9" || order.Status.ID == "10") {
		return etoroOrderState{}, c.lookupUnparsable("An eToro filled order has no execution records.")
	}
	var currency string
	if len(executions) > 0 {
		var portfolio struct {
			AccountCurrency string `json:"accountCurrency"`
		}
		_, err := c.client.Request(ctx, http.MethodGet,
			etoro.TradingPath(c.demo, "/api/v1/trading/info", "/aggregate-portfolio"), nil, &portfolio)
		if err != nil {
			if etoroDecodeFailure(err) {
				return etoroOrderState{}, fmt.Errorf("%w: %w", c.lookupUnparsable("An eToro aggregate portfolio could not be read."), err)
			}
			return etoroOrderState{}, err
		}
		currency = portfolio.AccountCurrency
		if len(currency) != 3 || strings.TrimSpace(currency) != currency ||
			strings.IndexFunc(currency, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
			return etoroOrderState{}, c.lookupUnparsable("An eToro aggregate portfolio has missing, blank or invalid accountCurrency.")
		}
	}
	return etoroOrderState{
		orderID: orderID, statusID: order.Status.ID, status: status, reason: order.Status.ErrorMessage,
		requested: requested, executions: executions, currency: currency,
	}, nil
}

// lookupClose resolves a v1 market-close order. eToro demo probe P2: orders:lookup
// answers 404 for a close orderID, and close-orders has no lookup by request id,
// so a close without a venue order id stays unresolved.
func (c *etoroConnector) lookupClose(ctx context.Context, ref fwtrading.OrderRef) (etoroOrderState, error) {
	if ref.VenueOrderID == "" {
		return etoroOrderState{}, c.lookupUnresolved()
	}
	orderID, err := etoroInteger(json.Number(ref.VenueOrderID), "close order id", true)
	if err != nil {
		return etoroOrderState{}, err
	}
	var response struct {
		OrderID      json.Number `json:"orderID"`
		StatusID     json.Number `json:"statusID"`
		ErrorMessage string      `json:"errorMessage"`
		Positions    *[]struct {
			PositionID json.Number `json:"positionID"`
			Units      json.Number `json:"units"`
			Rate       json.Number `json:"rate"`
			Occurred   time.Time   `json:"occurred"`
		} `json:"positions"`
	}
	_, err = c.client.Request(ctx, http.MethodGet, etoro.CloseOrderInfoPath(c.demo, orderID.String()), nil, &response)
	var apiErr *etoro.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return etoroOrderState{}, c.lookupUnresolved()
	}
	if err != nil {
		if etoroDecodeFailure(err) {
			return etoroOrderState{}, fmt.Errorf("%w: %w", c.lookupUnparsable("An eToro close-order response could not be read."), err)
		}
		return etoroOrderState{}, err
	}
	id, err := etoroInteger(response.OrderID, "close orderID", true)
	if err != nil || !id.Equal(orderID) || response.Positions == nil {
		return etoroOrderState{}, c.lookupUnparsable("An eToro close-order response has missing or inconsistent data.")
	}
	status, known := etoroVenueStatus(response.StatusID)
	if !known {
		return etoroOrderState{}, c.lookupUnparsable("An eToro close order returned an unknown status.")
	}
	filled := response.StatusID == "3"
	executions := make([]etoroExecution, 0, len(*response.Positions))
	requested := decimal.Zero
	for _, position := range *response.Positions {
		executed := position.Units != "" || position.Rate != "" || !position.Occurred.IsZero()
		if !filled {
			// close-orders reports no requested units, so only a filled close
			// proves its remaining quantity.
			if executed {
				return etoroOrderState{}, c.lookupUnparsable("An eToro close order reports executions before it filled.")
			}
			continue
		}
		positionID, err := etoroInteger(position.PositionID, "close positionID", true)
		if err != nil {
			return etoroOrderState{}, c.lookupUnparsable("An eToro close execution has an invalid positionID.")
		}
		quantity, err := etoroDecimal(position.Units, "close units")
		if err != nil {
			return etoroOrderState{}, c.lookupUnparsable("An eToro close execution has missing or invalid units.")
		}
		price, err := etoroDecimal(position.Rate, "close rate")
		if err != nil {
			return etoroOrderState{}, c.lookupUnparsable("An eToro close execution has missing or invalid rate.")
		}
		if !quantity.IsPositive() || !price.IsPositive() || position.Occurred.IsZero() {
			return etoroOrderState{}, c.lookupUnparsable("An eToro close execution has missing or invalid fields.")
		}
		// eToro demo probe P7: close-order info has no fee; Commission stays unset.
		executions = append(executions, etoroExecution{
			positionID: positionID, quantity: quantity, price: price, at: position.Occurred,
		})
		requested = requested.Add(quantity)
	}
	if filled && len(executions) == 0 {
		return etoroOrderState{}, c.lookupUnparsable("An eToro filled order has no execution records.")
	}
	return etoroOrderState{
		orderID: orderID, statusID: response.StatusID, status: status, reason: response.ErrorMessage,
		requested: requested, executions: executions,
	}, nil
}

func (c *etoroConnector) orderSnapshot(ref fwtrading.OrderRef, state etoroOrderState) (fwtrading.Snapshot, bool, error) {
	executions := state.executions
	// eToro demo probe P4: executions are immutable once reported.
	sort.SliceStable(executions, func(i, j int) bool {
		if executions[i].at.Equal(executions[j].at) {
			return executions[i].positionID.LessThan(executions[j].positionID)
		}
		return executions[i].at.Before(executions[j].at)
	})
	orderID := state.orderID.String()
	snapshot := fwtrading.Snapshot{VenueOrderID: orderID, Status: state.status, FilledQuantity: "0"}
	if state.statusID == "4" || state.statusID == "10" {
		snapshot.Reason = c.redact.Replace(state.reason)
	}
	cumulative := decimal.Zero
	for i, execution := range executions {
		cumulative = cumulative.Add(execution.quantity)
		leaves := state.requested.Sub(cumulative)
		if leaves.IsNegative() {
			return fwtrading.Snapshot{}, true, c.lookupUnparsable("eToro executions exceed requestedUnits.")
		}
		fill := fwtrading.Fill{
			Quantity: execution.quantity.String(), Price: execution.price.String(),
			CumQuantity: cumulative.String(), LeavesQuantity: leaves.String(),
			Final: i == len(executions)-1 && state.status == fwtrading.VenueStatusFilled,
			At:    execution.at,
		}
		if state.currency != "" {
			fill.Commission = &domain.Commission{Amount: execution.fee.String(), Currency: state.currency}
		}
		snapshot.Fills = append(snapshot.Fills, fill)
	}
	// eToro demo probe P8: a filled order's executions sum to its requested units (bestEffort under-execution unverified).
	if state.status == fwtrading.VenueStatusFilled && !cumulative.Equal(state.requested) {
		return fwtrading.Snapshot{}, true, c.lookupUnparsable("An eToro filled order's executions do not sum to requestedUnits.")
	}
	snapshot.FilledQuantity = cumulative.String()
	if state.status == fwtrading.VenueStatusFilled || state.status == fwtrading.VenueStatusCancelled {
		c.ordersMu.Lock()
		delete(c.orderIDs, orderID)
		if !slices.Contains(c.finished, orderID) {
			if len(c.finished) == etoroHeldLimit {
				c.finished = slices.Delete(c.finished, 0, 1)
			}
			c.finished = append(c.finished, orderID)
		}
		if i := slices.Index(c.held, orderID); i >= 0 {
			c.held = slices.Delete(c.held, i, i+1)
		}
		c.ordersMu.Unlock()
	} else {
		c.mapOrder(orderID, ref.ClientOrderID)
	}
	return snapshot, true, nil
}
