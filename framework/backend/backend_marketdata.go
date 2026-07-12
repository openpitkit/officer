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

package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
)

// MarketDataProvider is one built-in provider type the operator can configure.
type MarketDataProvider struct {
	Type  string
	Title string
}

// MarketDataInstrumentStatus is one configured instrument plus its latest quote
// snapshot, when one has been received. Stale is only true when both the source
// and instrument are enabled. UpdateInterval, when non-nil, is the elapsed time
// between the two most recent ticks of this instrument's quote, as observed by
// the running manager; it is nil until a second tick has arrived since the last
// (re)subscribe.
type MarketDataInstrumentStatus struct {
	Instrument     domain.MarketDataInstrument
	Quote          *domain.MarketDataQuote
	UpdateInterval *time.Duration
	Stale          bool
}

// MarketDataInstanceStatus is one configured source, all of its instruments,
// and its current subscription state. State is one of "disabled", "pending",
// "ok", or "error" (empty when no runtime is wired); Error carries the short
// operator-facing message when State is "error". References is nil when the
// connector exposes no help links. VerifiesSymbols is true when the provider
// can check whether an external symbol exists in its catalogue. SearchesSymbols
// is true when the provider can search its catalogue for matching instruments.
type MarketDataInstanceStatus struct {
	Instance        domain.MarketDataInstance
	Instruments     []MarketDataInstrumentStatus
	References      *marketdata.ProviderReferences
	VerifiesSymbols bool
	SearchesSymbols bool
	State           string
	Error           string
	Diagnostics     []marketdata.Diagnostic
}

// MarketDataStatus is the operator-facing market-data control-plane snapshot.
type MarketDataStatus struct {
	Providers        []MarketDataProvider
	Instances        []MarketDataInstanceStatus
	FreshnessSeconds int
	RestartRequired  bool
}

// ListMarketData returns configured providers, instances, instruments, and the
// latest quote snapshot for each configured instrument.
func (s *Service) ListMarketData(ctx context.Context) (MarketDataStatus, error) {
	n, err := s.groupNode()
	if err != nil {
		return MarketDataStatus{}, err
	}
	instances, err := n.ListMarketDataInstances(ctx)
	if err != nil {
		return MarketDataStatus{}, fmt.Errorf("backend: list market-data instances: %w", err)
	}
	quotes, err := n.ListMarketDataQuotes(ctx, domain.ExternalID(""))
	if err != nil {
		return MarketDataStatus{}, fmt.Errorf("backend: list market-data quotes: %w", err)
	}
	quoteByInstrument := make(map[string]domain.MarketDataQuote, len(quotes))
	for _, quote := range quotes {
		quoteByInstrument[marketDataKey(quote.Instance.String(), quote.ExternalSymbol)] = quote
	}

	var runtimeStatuses map[string]marketdata.InstanceRuntimeStatus
	var appliedConfig map[string]marketdata.AppliedInstanceConfig
	if s.md != nil {
		runtimeStatuses = s.md.InstanceStatuses()
		appliedConfig = s.md.AppliedConfig()
	}

	now := time.Now().UTC()
	statuses := make([]MarketDataInstanceStatus, 0, len(instances))
	currentConfig := make(map[string]marketdata.AppliedInstanceConfig, len(instances))
	for _, instance := range instances {
		instanceID := instance.ExternalID.String()
		instruments, err := n.ListMarketDataInstruments(ctx, instance.ExternalID)
		if err != nil {
			return MarketDataStatus{}, fmt.Errorf("backend: list market-data instruments: %w", err)
		}
		if instance.Enabled {
			currentConfig[instanceID] = marketDataAppliedConfig(instance, instruments)
		}
		instStatuses := make([]MarketDataInstrumentStatus, 0, len(instruments))
		for _, instrument := range instruments {
			var quotePtr *domain.MarketDataQuote
			if quote, ok := quoteByInstrument[marketDataKey(instrument.Instance.String(), instrument.ExternalSymbol)]; ok {
				q := quote
				quotePtr = &q
			}
			stale := marketDataInstrumentStale(
				instance.Enabled, instrument, quotePtr, now,
			)
			var interval *time.Duration
			if s.md != nil {
				if d, ok := s.md.QuoteUpdateInterval(
					instanceID, instrument.ExternalSymbol,
				); ok {
					interval = &d
				}
			}
			instStatuses = append(instStatuses, MarketDataInstrumentStatus{
				Instrument:     instrument,
				Quote:          quotePtr,
				UpdateInterval: interval,
				Stale:          stale,
			})
		}
		rt := runtimeStatuses[instanceID]
		state, errMsg := marketDataInstanceState(instance, s.md, runtimeStatuses)
		statuses = append(statuses, MarketDataInstanceStatus{
			Instance:        instance,
			Instruments:     instStatuses,
			References:      rt.References,
			VerifiesSymbols: s.registry.VerifiesSymbols(instance.Provider),
			SearchesSymbols: s.registry.SearchesSymbols(instance.Provider),
			State:           state,
			Error:           errMsg,
			Diagnostics:     rt.Diagnostics,
		})
	}
	return MarketDataStatus{
		Providers:        s.marketDataProviders(),
		Instances:        statuses,
		FreshnessSeconds: int(MarketDataFreshnessTTL.Seconds()),
		RestartRequired:  marketDataRestartRequired(s.md, currentConfig, appliedConfig),
	}, nil
}

// RestartMarketData re-applies the market-data configuration by stopping and
// restarting the connector manager. With no runtime wired it is a no-op.
//
// It re-adopts the node's current engine sink before restarting. Account,
// group, restore, and reset mutations rebuild the engine and replace its
// market-data service, which leaves any sink the manager cached pointing at a
// closed service (every push then fails with "market-data service is null").
// Re-adopting on restart converges every feed-change path - including the
// welcome flow - onto a restart that always wires the live sink.
func (s *Service) RestartMarketData(ctx context.Context) error {
	if s.md == nil {
		return nil
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	s.md.Stop()
	return s.restoreMarketDataAfterBackup(n.CurrentMarketDataSink())
}

// MarketDataSymbolVerification is the outcome of a one-shot symbol check for
// one instance. Supported is false when the instance's provider cannot verify
// symbols, in which case Exists and Suggestion are zero and the call still
// succeeds. Details carries optional provider metadata for the matched symbol.
// Suggestion is a case-folded catalogue variant when the symbol was not found
// as typed.
type MarketDataSymbolVerification struct {
	Supported  bool
	Exists     bool
	Suggestion string
	Details    string
}

// VerifyMarketDataSymbol checks whether externalSymbol exists on the identified
// instance's provider. It loads the instance config and runs a stateless probe
// (a fresh connector that is never subscribed), so live feeds are untouched. A
// provider that cannot verify symbols yields Supported=false with no error; an
// error is reserved for an unknown instance or a catalogue-fetch failure.
func (s *Service) VerifyMarketDataSymbol(
	ctx context.Context, id, externalSymbol string,
) (MarketDataSymbolVerification, error) {
	id = strings.TrimSpace(id)
	instanceID, err := domain.ParseExternalID(id)
	if err != nil {
		return MarketDataSymbolVerification{}, fmt.Errorf("market-data instance id: %w", err)
	}
	n, err := s.groupNode()
	if err != nil {
		return MarketDataSymbolVerification{}, err
	}
	instance, ok, err := n.GetMarketDataInstance(ctx, instanceID)
	if err != nil {
		return MarketDataSymbolVerification{}, fmt.Errorf("backend: get market-data instance: %w", err)
	}
	if !ok {
		return MarketDataSymbolVerification{}, fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	result, supported, err := s.registry.VerifySymbol(ctx, instance, strings.TrimSpace(externalSymbol))
	if err != nil {
		// A verify reaches the external provider; a provider/transport failure is
		// an expected operational condition, not a server bug, so it is surfaced
		// as upstream (502 + plain message) rather than a 500.
		return MarketDataSymbolVerification{}, fmt.Errorf(
			"%w: verify market-data symbol: %v", domain.ErrUpstream, err)
	}
	return MarketDataSymbolVerification{
		Supported:  supported,
		Exists:     result.Exists,
		Suggestion: result.Suggestion,
		Details:    result.Details,
	}, nil
}

// MarketDataSymbolMatch is one contract a symbol resolve returned. Name is the
// provider long name when available (empty otherwise); the remaining fields
// carry the resolved contract specifics.
type MarketDataSymbolMatch struct {
	Symbol                       string
	Name                         string
	SecType                      string
	Exchange                     string
	PrimaryExchange              string
	Currency                     string
	LastTradeDateOrContractMonth string
	Right                        string
	Multiplier                   string
	LocalSymbol                  string
	TradingClass                 string
	ConID                        string
	Strike                       string
}

// MarketDataSymbolSearch is the outcome of a symbol search for one instance.
// Supported is false when the instance's provider cannot search symbols, in
// which case Matches is empty and the call still succeeds.
type MarketDataSymbolSearch struct {
	Supported bool
	Matches   []MarketDataSymbolMatch
}

// MarketDataSymbolSearchInput carries the resolve criteria for one search: the
// typed Query (the symbol) plus the contract specifics that scope the resolve.
// Empty optional fields apply no constraint.
type MarketDataSymbolSearchInput struct {
	Query                        string
	SecType                      string
	Exchange                     string
	Currency                     string
	LastTradeDateOrContractMonth string
	Right                        string
	Strike                       string
}

// SearchMarketDataSymbols resolves the identified instance's provider catalogue
// for contracts matching the input criteria. It loads the instance config and
// runs a stateless probe (a fresh connector that is never subscribed), so live
// feeds are untouched. A provider that cannot search symbols yields
// Supported=false with no error; an error is reserved for an unknown instance or
// a transport failure. No matches is an empty slice with Supported=true.
func (s *Service) SearchMarketDataSymbols(
	ctx context.Context, id string, input MarketDataSymbolSearchInput,
) (MarketDataSymbolSearch, error) {
	id = strings.TrimSpace(id)
	instanceID, err := domain.ParseExternalID(id)
	if err != nil {
		return MarketDataSymbolSearch{}, fmt.Errorf("market-data instance id: %w", err)
	}
	n, err := s.groupNode()
	if err != nil {
		return MarketDataSymbolSearch{}, err
	}
	instance, ok, err := n.GetMarketDataInstance(ctx, instanceID)
	if err != nil {
		return MarketDataSymbolSearch{}, fmt.Errorf("backend: get market-data instance: %w", err)
	}
	if !ok {
		return MarketDataSymbolSearch{}, fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	matches, supported, err := s.registry.SearchSymbols(ctx, instance, marketdata.SymbolSearchQuery{
		Query:                        strings.TrimSpace(input.Query),
		SecType:                      strings.TrimSpace(input.SecType),
		Exchange:                     strings.TrimSpace(input.Exchange),
		Currency:                     strings.TrimSpace(input.Currency),
		LastTradeDateOrContractMonth: strings.TrimSpace(input.LastTradeDateOrContractMonth),
		Right:                        strings.TrimSpace(input.Right),
		Strike:                       input.Strike,
	})
	if err != nil {
		// Like verify, a resolve hits the external provider; a provider/transport
		// failure is upstream (502 + plain message), not a 500.
		return MarketDataSymbolSearch{}, fmt.Errorf(
			"%w: search market-data symbols: %v", domain.ErrUpstream, err)
	}
	out := MarketDataSymbolSearch{Supported: supported}
	if len(matches) > 0 {
		out.Matches = make([]MarketDataSymbolMatch, 0, len(matches))
		for _, match := range matches {
			out.Matches = append(out.Matches, MarketDataSymbolMatch{
				Symbol:                       match.Symbol,
				Name:                         match.Name,
				SecType:                      match.SecType,
				Exchange:                     match.Exchange,
				PrimaryExchange:              match.PrimaryExchange,
				Currency:                     match.Currency,
				LastTradeDateOrContractMonth: match.LastTradeDateOrContractMonth,
				Right:                        match.Right,
				Multiplier:                   match.Multiplier,
				LocalSymbol:                  match.LocalSymbol,
				TradingClass:                 match.TradingClass,
				ConID:                        match.ConID,
				Strike:                       match.Strike,
			})
		}
	}
	return out, nil
}

// marketDataInstanceState resolves one instance's operator-facing state: a
// disabled instance is "disabled"; with no runtime wired the state is empty; an
// enabled instance the manager has applied reports the manager's state (and
// error message on error); an enabled instance the manager has not yet applied
// is "pending", signalling the operator must restart.
func marketDataInstanceState(
	instance domain.MarketDataInstance,
	md MarketDataRuntime,
	runtimeStatuses map[string]marketdata.InstanceRuntimeStatus,
) (string, string) {
	if !instance.Enabled {
		return "disabled", ""
	}
	if md == nil {
		return "", ""
	}
	status, ok := runtimeStatuses[instance.ExternalID.String()]
	if !ok {
		return "pending", ""
	}
	if status.State == marketdata.StateError {
		return status.State, status.Error
	}
	return status.State, ""
}

func marketDataAppliedConfig(
	instance domain.MarketDataInstance,
	instruments []domain.MarketDataInstrument,
) marketdata.AppliedInstanceConfig {
	subs := make([]marketdata.Subscription, 0, len(instruments))
	for _, instrument := range instruments {
		if !instrument.Enabled {
			continue
		}
		subs = append(subs, marketdata.Subscription{
			External: instrument.ExternalSymbol,
			Base:     instrument.BaseAsset,
			Quote:    instrument.QuoteAsset,
		})
	}
	return marketdata.AppliedInstanceConfig{
		Provider:      instance.Provider,
		Subscriptions: subs,
	}
}

func marketDataRestartRequired(
	md MarketDataRuntime,
	current, applied map[string]marketdata.AppliedInstanceConfig,
) bool {
	if md == nil {
		return false
	}
	if len(current) != len(applied) {
		return true
	}
	for id, currentConfig := range current {
		appliedConfig, ok := applied[id]
		if !ok {
			return true
		}
		if currentConfig.Provider != appliedConfig.Provider {
			return true
		}
		if !sameMarketDataSubscriptions(
			currentConfig.Subscriptions, appliedConfig.Subscriptions,
		) {
			return true
		}
	}
	return false
}

func sameMarketDataSubscriptions(a, b []marketdata.Subscription) bool {
	if len(a) != len(b) {
		return false
	}
	left := marketDataSubscriptionKeys(a)
	right := marketDataSubscriptionKeys(b)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func marketDataSubscriptionKeys(subs []marketdata.Subscription) []string {
	keys := make([]string, 0, len(subs))
	for _, sub := range subs {
		keys = append(keys, sub.External+"\x00"+sub.Base+"\x00"+sub.Quote)
	}
	sort.Strings(keys)
	return keys
}

// CreateMarketDataInstance validates and persists one source instance. The
// operator may supply the instance's external id, which is used verbatim and
// must be canonical; a duplicate is rejected by the store with
// domain.ErrAlreadyExists. When the id is omitted (zero) the store mints one. The
// persisted instance is returned with its external id populated.
func (s *Service) CreateMarketDataInstance(
	ctx context.Context, instance domain.MarketDataInstance,
) (domain.MarketDataInstance, error) {
	instance.Provider = strings.TrimSpace(instance.Provider)
	instance.Label = strings.TrimSpace(instance.Label)
	instance.Credentials = strings.TrimSpace(instance.Credentials)
	title, ok := s.marketDataProviderTitle(instance.Provider)
	if !ok {
		return domain.MarketDataInstance{},
			fmt.Errorf("market-data provider %q: %w", instance.Provider, domain.ErrInvalid)
	}
	if instance.Label == "" {
		instance.Label = title
	}
	n, err := s.groupNode()
	if err != nil {
		return domain.MarketDataInstance{}, err
	}
	instances, err := n.ListMarketDataInstances(ctx)
	if err != nil {
		return domain.MarketDataInstance{}, fmt.Errorf("backend: list market-data instances: %w", err)
	}
	if marketDataLabelTaken(instances, instance.Label) {
		return domain.MarketDataInstance{},
			fmt.Errorf("market-data source label %q: %w", instance.Label, domain.ErrAlreadyExists)
	}
	if err := validateMarketDataInstance(s.registry, instance); err != nil {
		return domain.MarketDataInstance{}, err
	}
	return n.CreateMarketDataInstance(ctx, instance, auth.CallerFromContext(ctx))
}

// SetMarketDataInstanceEnabled toggles one source instance.
func (s *Service) SetMarketDataInstanceEnabled(
	ctx context.Context, id string, enabled bool,
) error {
	instanceID, err := domain.ParseExternalID(strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("market-data instance id: %w", err)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetMarketDataInstanceEnabled(ctx, instanceID, enabled, auth.CallerFromContext(ctx))
}

// UpdateMarketDataInstanceSettings validates and persists editable source
// settings. Empty values in the credentials update preserve existing keys so
// the UI can submit blank password fields without fetching stored secrets.
func (s *Service) UpdateMarketDataInstanceSettings(
	ctx context.Context, id, label, credentials string,
) error {
	label = strings.TrimSpace(label)
	credentials = strings.TrimSpace(credentials)
	instanceID, err := domain.ParseExternalID(strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("market-data instance id: %w", err)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	instance, ok, err := n.GetMarketDataInstance(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("backend: get market-data instance: %w", err)
	}
	if !ok {
		return fmt.Errorf("market-data instance %q: %w", id, domain.ErrNotFound)
	}
	if label == "" {
		label = instance.Label
	}
	instances, err := n.ListMarketDataInstances(ctx)
	if err != nil {
		return fmt.Errorf("backend: list market-data instances: %w", err)
	}
	if marketDataLabelTakenExcept(instances, instanceID, label) {
		return fmt.Errorf("market-data source label %q: %w", label, domain.ErrAlreadyExists)
	}
	mergedCredentials, err := mergeMarketDataCredentials(instance.Credentials, credentials)
	if err != nil {
		return err
	}
	instance.Label = label
	instance.Credentials = mergedCredentials
	if err := validateMarketDataInstance(s.registry, instance); err != nil {
		return err
	}
	return n.UpdateMarketDataInstanceSettings(
		ctx, instanceID, instance.Label, instance.Credentials, auth.CallerFromContext(ctx),
	)
}

// DeleteMarketDataInstance removes one source instance and its feed-owned
// instruments and quotes.
func (s *Service) DeleteMarketDataInstance(
	ctx context.Context, id string,
) error {
	instanceID, err := domain.ParseExternalID(strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("market-data instance id: %w", err)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.DeleteMarketDataInstance(ctx, instanceID, auth.CallerFromContext(ctx))
}

// UpsertMarketDataInstrument validates and persists one instrument mapping,
// then pushes its operator-set manual mark once into the running instance. The
// push is a no-op for streaming providers and for instruments without a manual
// price (see MarketDataRuntime.PushManual), so only a manual instrument with a
// price reaches the engine, and exactly once per upsert.
func (s *Service) UpsertMarketDataInstrument(
	ctx context.Context, instrument domain.MarketDataInstrument,
) error {
	instrument.ExternalSymbol = strings.TrimSpace(instrument.ExternalSymbol)
	instrument.BaseAsset = strings.TrimSpace(instrument.BaseAsset)
	instrument.QuoteAsset = strings.TrimSpace(instrument.QuoteAsset)
	instrument.ManualPrice = strings.TrimSpace(instrument.ManualPrice)
	if err := validateMarketDataInstrument(instrument); err != nil {
		return err
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	if err := n.UpsertMarketDataInstrument(ctx, instrument, auth.CallerFromContext(ctx)); err != nil {
		return err
	}
	if s.md != nil {
		s.md.PushManual(instrument.Instance.String(), instrument)
	}
	return nil
}

// SetMarketDataInstrumentEnabled toggles one instrument mapping.
func (s *Service) SetMarketDataInstrumentEnabled(
	ctx context.Context, instanceID, externalSymbol string, enabled bool,
) error {
	externalSymbol = strings.TrimSpace(externalSymbol)
	instance, err := domain.ParseExternalID(strings.TrimSpace(instanceID))
	if err != nil {
		return fmt.Errorf("market-data instrument: %w", err)
	}
	if externalSymbol == "" {
		return fmt.Errorf("market-data instrument: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.SetMarketDataInstrumentEnabled(
		ctx, instance, externalSymbol, enabled, auth.CallerFromContext(ctx),
	)
}

// DeleteMarketDataInstrument removes one instrument mapping.
func (s *Service) DeleteMarketDataInstrument(
	ctx context.Context, instanceID, externalSymbol string,
) error {
	externalSymbol = strings.TrimSpace(externalSymbol)
	instance, err := domain.ParseExternalID(strings.TrimSpace(instanceID))
	if err != nil {
		return fmt.Errorf("market-data instrument: %w", err)
	}
	if externalSymbol == "" {
		return fmt.Errorf("market-data instrument: %w", domain.ErrInvalid)
	}
	n, err := s.groupNode()
	if err != nil {
		return err
	}
	return n.DeleteMarketDataInstrument(
		ctx, instance, externalSymbol, auth.CallerFromContext(ctx),
	)
}

func (s *Service) marketDataProviders() []MarketDataProvider {
	providers := s.registry.List()
	out := make([]MarketDataProvider, 0, len(providers))
	for _, provider := range providers {
		out = append(out, MarketDataProvider{
			Type:  provider.Type,
			Title: provider.Title,
		})
	}
	return out
}

func (s *Service) marketDataProviderTitle(typ string) (string, bool) {
	for _, provider := range s.marketDataProviders() {
		if typ == provider.Type {
			return provider.Title, true
		}
	}
	return "", false
}

func marketDataLabelTaken(instances []domain.MarketDataInstance, label string) bool {
	label = strings.TrimSpace(label)
	for _, instance := range instances {
		if strings.EqualFold(strings.TrimSpace(instance.Label), label) {
			return true
		}
	}
	return false
}

func marketDataLabelTakenExcept(
	instances []domain.MarketDataInstance, id domain.ExternalID, label string,
) bool {
	label = strings.TrimSpace(label)
	for _, instance := range instances {
		if instance.ExternalID == id {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(instance.Label), label) {
			return true
		}
	}
	return false
}

func mergeMarketDataCredentials(existing, update string) (string, error) {
	existing = strings.TrimSpace(existing)
	update = strings.TrimSpace(update)
	if update == "" {
		return existing, nil
	}
	merged := make(map[string]any)
	if existing != "" {
		if err := json.Unmarshal([]byte(existing), &merged); err != nil {
			return "", fmt.Errorf("market-data stored credentials: %w", domain.ErrInvalid)
		}
	}
	var incoming map[string]any
	if err := json.Unmarshal([]byte(update), &incoming); err != nil {
		return "", fmt.Errorf("market-data credentials: %w", domain.ErrInvalid)
	}
	for key, value := range incoming {
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			if _, exists := merged[key]; exists {
				continue
			}
		}
		merged[key] = value
	}
	if len(merged) == 0 {
		return "", nil
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return "", fmt.Errorf("market-data credentials: %w", err)
	}
	return string(out), nil
}

func validateMarketDataInstance(registry *marketdata.Registry, instance domain.MarketDataInstance) error {
	if instance.Label == "" {
		return fmt.Errorf("market-data source label: %w", domain.ErrInvalid)
	}
	if !registry.Known(instance.Provider) {
		return fmt.Errorf("market-data provider %q: %w", instance.Provider, domain.ErrInvalid)
	}
	if instance.Credentials != "" && !json.Valid([]byte(instance.Credentials)) {
		return fmt.Errorf("market-data credentials: %w", domain.ErrInvalid)
	}
	if err := registry.Validate(instance); err != nil {
		return fmt.Errorf("market-data credentials: %v: %w", err, domain.ErrInvalid)
	}
	return nil
}

func validateMarketDataInstrument(instrument domain.MarketDataInstrument) error {
	if instrument.Instance.IsZero() || instrument.ExternalSymbol == "" {
		return fmt.Errorf("market-data instrument: %w", domain.ErrInvalid)
	}
	if err := domain.ValidateAsset(instrument.BaseAsset); err != nil {
		return err
	}
	if err := domain.ValidateAsset(instrument.QuoteAsset); err != nil {
		return err
	}
	if err := domain.ValidateMarketDataMark(instrument.ManualPrice); err != nil {
		return err
	}
	return nil
}

func marketDataInstrumentStale(
	instanceEnabled bool,
	instrument domain.MarketDataInstrument,
	quote *domain.MarketDataQuote,
	now time.Time,
) bool {
	if !instanceEnabled || !instrument.Enabled {
		return false
	}
	if quote == nil {
		return true
	}
	return now.Sub(quote.AsOf) > MarketDataFreshnessTTL
}

func marketDataKey(instanceID, externalSymbol string) string {
	return instanceID + "\x00" + externalSymbol
}
