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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// Store is the read seam the manager needs. It reads the complete configured
// pair set to plan synthetic inverses, then starts only enabled instances and
// instruments. It is the subset of the officer Store the runtime consumes; the
// concrete sqlite store satisfies it.
type Store interface {
	// ListMarketDataInstances returns every configured market-data instance.
	ListMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)
	// ListEnabledMarketDataInstances returns every enabled market-data instance.
	ListEnabledMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)
	// ListMarketDataInstruments returns every configured instrument of the
	// identified instance.
	ListMarketDataInstruments(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataInstrument, error)
	// ListEnabledMarketDataInstruments returns the enabled instruments of the
	// identified instance.
	ListEnabledMarketDataInstruments(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataInstrument, error)
}

// Manager owns the runtime lifecycle of every enabled market-data connector. It
// reads the enabled config from the store, builds one connector per instance by
// type, subscribes it to that instance's enabled instruments, and drains each
// connector's channel into the sink. Symbol mapping (instrument rows ->
// Subscriptions) lives here; connectors tag emitted quotes with base/quote.
//
// Graceful degradation is the contract: nothing enabled is a clean no-op, and a
// single bad instance (unknown type, subscribe error) is logged and skipped
// without blocking the others or crashing the process.
type Manager struct {
	registry  *Registry
	store     Store
	sink      Sink
	stopGrace time.Duration
	// manualRefreshInterval keeps configured BYO marks alive inside the engine's
	// freshness window. It is an instance field so tests can shorten it without
	// changing process-wide timing.
	manualRefreshInterval time.Duration
	// sinkProvider, when set, resolves the live engine sink on every push. The
	// normal account, group, and asset paths preserve the engine and sink, while
	// residual administrative and recovery rebuilds recreate them. Resolving per
	// push keeps quotes flowing across those rebuilds without a manager restart.
	// It takes precedence over the static sink.
	sinkProvider func() Sink
	logger       *slog.Logger

	mu             sync.Mutex
	baseCtx        context.Context
	started        bool
	cancel         context.CancelFunc
	runWG          *sync.WaitGroup
	connectors     []Connector
	appliedConfig  map[string]AppliedInstanceConfig
	syntheticPairs map[quoteInstrumentKey]struct{}
	// byInstance maps an instance id to its live connector, so a manual quote
	// can be pushed into a specific running instance's connector after startup.
	// Only push-capable (Pushable) connectors are entered.
	byInstance map[string]Pushable
	// manualMarks is the currently desired BYO mark per applied pair. The drain
	// checks it before forwarding a queued manual refresh, so a tick read before
	// a live edit cannot resurrect an older mark after the edit or clear.
	manualMarks map[string]map[quoteInstrumentKey]string
	// manualRevisions invalidates a refresh snapshot when a live upsert lands
	// after the refresh began reading the store.
	manualRevisions map[string]uint64
	manualTokens    map[string]map[quoteInstrumentKey]uint64
	manualSynthetic map[string]map[quoteInstrumentKey]bool
	statuses        map[string]InstanceRuntimeStatus
	latestQuotes    map[quoteSnapshotIdentity]domain.MarketDataQuote
	// The publisher gate preserves FIFO across live upserts and periodic
	// reconciliation while allowing a cancelled caller to stop waiting. The
	// apply gate orders every sink mutation when Stop times out. The per-run
	// sink barrier holds every new feed mutation until inherited clears finish.
	manualPublisherGate   chan struct{}
	sinkApplyGate         chan struct{}
	manualGeneration      uint64
	nextManualToken       uint64
	manualAcknowledged    map[manualQuoteIdentity]manualAcknowledgedState
	manualMayBeLive       map[manualQuoteIdentity]manualAcknowledgedState
	pendingManualClears   map[manualQuoteIdentity]manualClearState
	sinkBarrier           chan struct{}
	sinkBarrierGeneration uint64
	sinkBarrierPending    map[manualQuoteIdentity]uint64

	// intervalMu guards the inter-update interval state below. The drain validates
	// its run under mu before mutating it, then uses this dedicated lock for the
	// per-tick values keyed by intervalKey(instanceID, external).
	intervalMu   sync.Mutex
	lastArrival  map[string]time.Time
	lastInterval map[string]time.Duration
}

// AppliedInstanceConfig is the enabled market-data configuration the manager
// applied during the latest Start. Backend status compares it to the stored
// configuration to decide whether a feed restart is required.
type AppliedInstanceConfig struct {
	Provider      string
	Subscriptions []Subscription
}

type manualQuoteIdentity struct {
	instanceID string
	pair       quoteInstrumentKey
}

type manualAcknowledgedState struct {
	mark      string
	synthetic bool
	external  string
}

type manualClearState struct {
	generation uint64
	token      uint64
	direct     bool
	synthetic  bool
	external   string
}

type startupManualAction struct {
	instanceID string
	external   string
	pushable   Pushable
	update     QuoteUpdate
}

// InstanceRuntimeStatus is the current (not historical) runtime state of one
// instance's subscription. State is StateOK or StateError; Error is set only
// for StateError. Diagnostics carries the recent problem messages the manager
// and connectors produced for the instance, newest last. References is set when
// the connector implements Referenceable and returned at least one non-empty URL;
// it is nil otherwise. VerifiesSymbols is true when the connector implements
// SymbolVerifier, i.e. the provider can check whether an external symbol exists.
// SearchesSymbols is true when the connector implements SymbolSearcher, i.e. the
// provider can search its catalogue for matching instruments.
type InstanceRuntimeStatus struct {
	State           string
	Error           string
	References      *ProviderReferences
	VerifiesSymbols bool
	SearchesSymbols bool
	Diagnostics     []Diagnostic
}

// QuoteSnapshot carries a last accepted quote with its resolved engine asset pair.
// BaseAssetID and QuoteAssetID are routing identity, not display data.
type QuoteSnapshot struct {
	domain.MarketDataQuote
	BaseAssetID  domain.EngineAssetID
	QuoteAssetID domain.EngineAssetID
}

const (
	StateOK    = "ok"
	StateError = "error"
)

// diagBufferCap bounds the per-instance diagnostic buffer; the oldest entries
// are dropped beyond it.
const diagBufferCap = 20

// diagnoseGrace is the idle window after Subscribe before the framework checks
// whether any data has arrived and (if not) triggers one-shot self-diagnosis.
const diagnoseGrace = 20 * time.Second

const defaultManagerStopGrace = 2 * time.Second

// ErrUnknownAsset marks an engine asset identifier that the market-data
// boundary can no longer resolve.
var ErrUnknownAsset = errors.New("marketdata: unknown asset")

// ErrUnsupportedProvider marks an unregistered market-data provider type.
var ErrUnsupportedProvider = errors.New("unsupported market-data provider")

// ErrNilSink reports a missing quote sink at the market-data manager boundary.
var ErrNilSink = errors.New("marketdata: nil sink")

// NewManager builds a manager over the registry, store, sink, and logger.
// registry is first because it owns connector construction.
func NewManager(
	registry *Registry,
	store Store,
	sink Sink,
	logger *slog.Logger,
) (*Manager, error) {
	if sink == nil {
		return nil, ErrNilSink
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(discard{}, nil))
	}
	if registry == nil {
		registry = NewRegistry()
	}
	return &Manager{
		registry:              registry,
		store:                 store,
		sink:                  sink,
		stopGrace:             defaultManagerStopGrace,
		manualRefreshInterval: FreshnessTTL / 2,
		logger:                logger,
		manualPublisherGate:   newManagerGate(),
		sinkApplyGate:         newManagerGate(),
		manualAcknowledged:    make(map[manualQuoteIdentity]manualAcknowledgedState),
		manualMayBeLive:       make(map[manualQuoteIdentity]manualAcknowledgedState),
		latestQuotes:          make(map[quoteSnapshotIdentity]domain.MarketDataQuote),
	}, nil
}

func newManagerGate() chan struct{} {
	gate := make(chan struct{}, 1)
	gate <- struct{}{}
	return gate
}

// Registry returns the connector registry used by this manager.
func (m *Manager) Registry() *Registry {
	return m.registry
}

// Start reads the enabled instances and brings up one connector per instance.
// It is safe to call once; a second Start while running is a no-op. An instance
// whose type is unknown, whose instruments become unreadable after reconciliation
// has read them, or whose connector cannot be built or subscribed is logged and
// skipped - these later per-instance failures never fail Start or stop the other
// instances.
// Reconciliation must read every enabled instrument; a store read failure fails
// Start because a partial allowed set could clear a configured quote.
// Start waits at most 2 x stopGrace for the sink apply gate and fails explicitly
// when an in-flight engine sink operation still holds it, so a stuck sink cannot
// wedge the caller's lock. The condition is transient and the call may be
// retried.
// With nothing enabled Start brings up no connector but still reconciles: every
// quote identity left in the registry is cleared.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	sink := m.currentSink()
	if sink == nil {
		return ErrNilSink
	}
	gateCtx, cancelGateWait := context.WithTimeout(ctx, 2*m.stopGrace)
	gateErr := m.acquireGate(gateCtx, m.sinkApplyGate)
	cancelGateWait()
	if gateErr != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf(
			"marketdata: restart blocked by an in-flight engine sink operation: %w",
			gateErr,
		)
	}
	defer m.releaseGate(m.sinkApplyGate)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}
	m.baseCtx = ctx

	instances, err := m.store.ListEnabledMarketDataInstances(ctx)
	if err != nil {
		return fmt.Errorf("marketdata: list enabled instances: %w", err)
	}
	allowedQuoteIdentities, err := m.allowedQuoteIdentities(ctx, instances)
	if err != nil {
		return err
	}
	syntheticPairs, err := m.syntheticPairPlan(ctx)
	if err != nil {
		return err
	}
	if err := m.reconcileQuoteRegistry(
		sink, allowedQuoteIdentities, syntheticPairs,
	); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	runWG := &sync.WaitGroup{}
	m.cancel = cancel
	m.runWG = runWG
	m.started = true
	m.manualGeneration++
	generation := m.manualGeneration
	m.statuses = make(map[string]InstanceRuntimeStatus, len(instances))
	m.byInstance = make(map[string]Pushable, len(instances))
	m.manualMarks = make(map[string]map[quoteInstrumentKey]string, len(instances))
	m.manualRevisions = make(map[string]uint64, len(instances))
	m.manualTokens = make(map[string]map[quoteInstrumentKey]uint64, len(instances))
	m.manualSynthetic = make(map[string]map[quoteInstrumentKey]bool, len(instances))
	m.pendingManualClears = make(map[manualQuoteIdentity]manualClearState)
	m.sinkBarrier = make(chan struct{})
	m.sinkBarrierGeneration = generation
	m.sinkBarrierPending = make(map[manualQuoteIdentity]uint64)
	m.appliedConfig = make(map[string]AppliedInstanceConfig, len(instances))
	m.syntheticPairs = syntheticPairs

	m.intervalMu.Lock()
	m.lastArrival = make(map[string]time.Time)
	m.lastInterval = make(map[string]time.Duration)
	m.intervalMu.Unlock()

	for _, instance := range instances {
		m.startInstanceLocked(
			runCtx, runWG, generation, instance, m.sinkBarrier,
		)
	}
	startupActions := m.startupManualActionsLocked(generation)
	for _, action := range startupActions {
		if !action.update.clear {
			continue
		}
		m.sinkBarrierPending[manualQuoteIdentity{
			instanceID: action.instanceID,
			pair: quoteInstrumentKey{
				base: action.update.Base, quote: action.update.Quote,
			},
		}] = action.update.manualToken
	}
	runWG.Add(1)
	go m.runStartupManualActions(
		runCtx, runWG, generation, m.sinkBarrier, startupActions,
	)
	if m.manualRefreshInterval > 0 &&
		(len(m.byInstance) > 0 || len(m.pendingManualClears) > 0) {
		runWG.Add(1)
		go m.refreshManualMarks(runCtx, runWG, m.manualRefreshInterval)
	}
	return nil
}

func (m *Manager) allowedQuoteIdentities(
	ctx context.Context,
	instances []domain.MarketDataInstance,
) (map[quoteSnapshotIdentity]struct{}, error) {
	allowed := make(map[quoteSnapshotIdentity]struct{})
	for _, instance := range instances {
		instruments, err := m.store.ListEnabledMarketDataInstruments(
			ctx, instance.ExternalID,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"marketdata: list enabled instruments for %s: %w",
				instance.ExternalID, err,
			)
		}
		for _, instrument := range instruments {
			allowed[quoteSnapshotIdentity{
				instanceID: instance.ExternalID.String(),
				external:   instrument.ExternalSymbol,
				pair: quoteInstrumentKey{
					base:  instrument.BaseAssetID,
					quote: instrument.QuoteAssetID,
				},
			}] = struct{}{}
		}
	}
	return allowed, nil
}

func (m *Manager) reconcileQuoteRegistry(
	sink Sink,
	allowed map[quoteSnapshotIdentity]struct{},
	syntheticPairs map[quoteInstrumentKey]struct{},
) error {
	directAllowedPairs := make(map[quoteInstrumentKey]struct{}, len(allowed))
	for identity := range allowed {
		directAllowedPairs[identity.pair] = struct{}{}
	}
	inverseAllowedPairs := make(
		map[quoteInstrumentKey]struct{}, 2*len(directAllowedPairs),
	)
	for pair := range directAllowedPairs {
		inverseAllowedPairs[pair] = struct{}{}
		if _, synthetic := syntheticPairs[pair]; !synthetic {
			continue
		}
		inverseAllowedPairs[quoteInstrumentKey{
			base: pair.quote, quote: pair.base,
		}] = struct{}{}
	}

	stale := make([]quoteSnapshotIdentity, 0)
	for identity := range m.latestQuotes {
		if _, ok := allowed[identity]; !ok {
			stale = append(stale, identity)
		}
	}

	type reconciliationClear struct {
		pair    quoteInstrumentKey
		inverse bool
	}
identityLoop:
	for _, identity := range stale {
		clears := make([]reconciliationClear, 0, 2)
		if _, ok := directAllowedPairs[identity.pair]; !ok {
			clears = append(clears, reconciliationClear{pair: identity.pair})
		}
		inverse := quoteInstrumentKey{
			base: identity.pair.quote, quote: identity.pair.base,
		}
		if _, ok := inverseAllowedPairs[inverse]; !ok {
			clears = append(clears, reconciliationClear{
				pair: inverse, inverse: true,
			})
		}
		if len(clears) > 0 {
			clearer, ok := sink.(QuoteClearer)
			if !ok {
				return fmt.Errorf("marketdata: sink does not support quote clear")
			}
			for _, clear := range clears {
				err := clearer.Clear(clear.pair.base, clear.pair.quote)
				if errors.Is(err, ErrUnknownAsset) {
					m.logger.Warn(
						"market data reconciliation dropped unresolvable quote identity",
						"instance_id", identity.instanceID,
						"external_symbol", identity.external,
						"base_asset_id", clear.pair.base,
						"quote_asset_id", clear.pair.quote,
						"inverse", clear.inverse,
						"reason", err,
					)
					m.retireManualQuoteLocked(manualQuoteIdentity{
						instanceID: identity.instanceID,
						pair:       identity.pair,
					})
					continue identityLoop
				}
				if err != nil {
					direction := "quote"
					if clear.inverse {
						direction = "inverse quote"
					}
					return fmt.Errorf(
						"marketdata: reconcile %s %s/%s pair %d/%d: %w",
						direction, identity.instanceID, identity.external,
						clear.pair.base, clear.pair.quote, err,
					)
				}
			}
		}
		delete(m.latestQuotes, identity)
	}
	return nil
}

// startInstanceLocked brings up one instance: read its enabled instruments,
// build the connector, subscribe, and drain into the sink. Any failure records
// a diagnostic (logged via recordDiagLocked) and skips the instance. Callers
// must hold mu.
func (m *Manager) startInstanceLocked(
	ctx context.Context,
	runWG *sync.WaitGroup,
	generation uint64,
	instance domain.MarketDataInstance,
	sinkBarrier <-chan struct{},
) {
	// Use the string form of the external id as the runtime map key.
	instanceID := instance.ExternalID.String()

	// Drop any interval state from a prior run of this instance so a restart
	// never surfaces a pre-restart gap.
	m.clearInstanceIntervals(instanceID)
	instruments, err := m.store.ListEnabledMarketDataInstruments(ctx, instance.ExternalID)
	if err != nil {
		msg := fmt.Sprintf("read instruments: %v", err)
		m.statuses[instanceID] = InstanceRuntimeStatus{State: StateError, Error: msg}
		m.recordDiagLocked(instanceID, Diagnostic{
			Level:       DiagError,
			Code:        CodeInternalError,
			Kind:        DiagKindProvider,
			Title:       "Internal error",
			Detail:      msg,
			Remediation: "Try Restart feeds; if it persists, contact support.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}},
		})
		return
	}
	if len(instruments) == 0 {
		m.appliedConfig[instanceID] = AppliedInstanceConfig{
			Provider: instance.Provider,
			// No enabled instruments still applies a config: carry a non-nil empty
			// subscription slice so the applied shape stays a slice, never nil.
			Subscriptions: []Subscription{},
		}
		m.statuses[instanceID] = InstanceRuntimeStatus{
			State: StateError,
			Error: "no enabled instruments",
		}
		m.recordDiagLocked(instanceID, Diagnostic{
			Level:       DiagError,
			Code:        CodeNoEnabledInstruments,
			Kind:        DiagKindConfig,
			Title:       "No instruments enabled",
			Detail:      fmt.Sprintf("Provider %q has no enabled instruments.", instance.Provider),
			Remediation: "Add or enable at least one instrument, then Restart feeds.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}},
		})
		return
	}

	connector, err := m.registry.Build(instance)
	if err != nil {
		if errors.Is(err, ErrUnsupportedProvider) {
			msg := fmt.Sprintf("unsupported provider type %q", instance.Provider)
			m.statuses[instanceID] = InstanceRuntimeStatus{State: StateError, Error: msg}
			m.recordDiagLocked(instanceID, Diagnostic{
				Level:       DiagError,
				Code:        CodeUnsupportedProvider,
				Kind:        DiagKindConfig,
				Title:       "Unsupported provider",
				Detail:      fmt.Sprintf("Provider type %q is not supported.", instance.Provider),
				Remediation: "Choose a supported provider.",
			})
			return
		}

		msg := fmt.Sprintf("connector config: %v", err)
		m.statuses[instanceID] = InstanceRuntimeStatus{State: StateError, Error: msg}
		m.recordDiagLocked(instanceID, Diagnostic{
			Level:       DiagError,
			Code:        CodeInvalidProviderConfig,
			Kind:        DiagKindConfig,
			Title:       "Invalid provider configuration",
			Detail:      fmt.Sprintf("Provider %q: connector config: %v", instance.Provider, err),
			Remediation: "Update this provider's credentials or settings, then Restart feeds.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}},
		})
		return
	}

	// Capture provider capabilities while holding mu (we are already holding it):
	// help links and whether the provider can verify symbol existence. Both are
	// optional connector interfaces detected by type assertion.
	var refs *ProviderReferences
	if r, ok := connector.(Referenceable); ok {
		if got, ok := r.References(); ok && (got.DocsURL != "" || got.SymbolsURL != "") {
			refs = &got
		}
	}
	_, verifiesSymbols := connector.(SymbolVerifier)
	_, searchesSymbols := connector.(SymbolSearcher)

	// Wire reporters before Subscribe so messages from the connector's background
	// goroutine route correctly from the first instant. Both reporters lock mu
	// themselves; they are only called from the background goroutine, never
	// synchronously from Set* or Subscribe, so there is no re-entrant lock.
	if sr, ok := connector.(StatusReporting); ok {
		sr.SetStatusReporter(m.reporterFor(instanceID, generation))
	}
	if dr, ok := connector.(DiagnosticReporting); ok {
		id := instanceID
		dr.SetDiagnosticReporter(func(diag Diagnostic) {
			m.recordDiagForGeneration(generation, id, diag)
		})
	}

	subs := subscriptionsFor(instruments, m.syntheticPairs)
	m.appliedConfig[instanceID] = AppliedInstanceConfig{
		Provider:      instance.Provider,
		Subscriptions: cloneSubscriptions(subs),
	}
	symbols := externalSymbolsFor(subs)
	ch, err := connector.Subscribe(ctx, subs)
	if err != nil {
		connector.Close()
		msg := fmt.Sprintf("subscribe failed: %v", err)
		m.statuses[instanceID] = InstanceRuntimeStatus{
			State: StateError, Error: msg, References: refs,
			VerifiesSymbols: verifiesSymbols, SearchesSymbols: searchesSymbols,
		}
		m.recordDiagLocked(instanceID, Diagnostic{
			Level:       DiagError,
			Code:        CodeConnectionError,
			Kind:        DiagKindEnvironment,
			Title:       "Subscription failed",
			Detail:      msg,
			Remediation: "Check the source is reachable, then Restart feeds.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
		})
		return
	}

	// Build the set of expected stable instrument keys for the watchdog.
	expectedKeys := make([]quoteInstrumentKey, 0, len(instruments))
	for _, inst := range instruments {
		expectedKeys = append(expectedKeys, quoteInstrumentKey{
			base: inst.BaseAssetID, quote: inst.QuoteAssetID,
		})
	}

	// received tracks which instrument keys have delivered at least one update.
	// sync.Map is used so drain can write and the watchdog can read concurrently
	// without additional locks.
	received := &sync.Map{}
	m.connectors = append(m.connectors, connector)
	runWG.Add(1)
	go m.drain(
		ctx, runWG, generation, instance.ExternalID, symbols, m.syntheticPairs, ch, received,
		sinkBarrier,
	)
	m.statuses[instanceID] = InstanceRuntimeStatus{
		State: StateOK, References: refs,
		VerifiesSymbols: verifiesSymbols, SearchesSymbols: searchesSymbols,
	}

	// Push-capable connectors (BYO) accept operator-set manual marks. Record the
	// handle so a later upsert can push into this running instance, and re-apply
	// every stored manual price now as a single quote each. The push drains
	// through the same channel as any quote; streaming connectors are not
	// Pushable and carry no manual prices, so they are untouched.
	pushable, pushableOK := connector.(Pushable)
	pushableOK = pushableOK && instance.Provider == domain.MarketDataProviderBYO
	if pushableOK {
		m.byInstance[instanceID] = pushable
		m.manualMarks[instanceID] = make(map[quoteInstrumentKey]string, len(instruments))
		m.manualTokens[instanceID] = make(map[quoteInstrumentKey]uint64, len(instruments))
		m.manualSynthetic[instanceID] = make(map[quoteInstrumentKey]bool, len(instruments))
		for _, inst := range instruments {
			key := quoteInstrumentKey{
				base: inst.BaseAssetID, quote: inst.QuoteAssetID,
			}
			m.manualMarks[instanceID][key] = inst.ManualPrice
			m.manualTokens[instanceID][key] = m.nextManualTokenLocked()
			_, synthetic := m.syntheticPairs[key]
			m.manualSynthetic[instanceID][key] = synthetic
		}
	}
	if pushableOK {
		return
	}

	// One-shot no-data watchdog: after diagnoseGrace, if any expected instrument
	// has not delivered a quote, run self-diagnosis (if available) or record a
	// generic no-data warning. Runs once; tracked on runWG so Stop waits for it.
	id := instanceID
	runWG.Add(1)
	go func() {
		defer runWG.Done()
		select {
		case <-ctx.Done():
			return
		case <-time.After(diagnoseGrace):
		}
		// Check whether any expected instrument is still silent.
		silent := false
		for _, key := range expectedKeys {
			if _, ok := received.Load(key); !ok {
				silent = true
				break
			}
		}
		if !silent {
			return
		}
		if d, ok := connector.(Diagnosable); ok {
			// Diagnose off-lock; recordDiag locks internally.
			findings, err := d.Diagnose(ctx)
			if err != nil {
				m.recordDiagForGeneration(generation, id, Diagnostic{
					Level:       DiagError,
					Code:        CodeSelfDiagnosisFailed,
					Kind:        DiagKindProvider,
					Title:       "Self-diagnosis failed",
					Detail:      err.Error(),
					Remediation: "Couldn't validate against the provider; try Restart feeds.",
					Actions:     []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
				})
				return
			}
			if len(findings) == 0 {
				// A provider that tolerates silence (e.g. equities outside
				// market hours) treats a quiet-but-valid feed as normal, so the
				// generic no-data warning would only be noise; liveness is
				// confirmed on demand via the per-symbol verify button.
				if st, ok := connector.(SilenceTolerant); ok && st.ToleratesSilence() {
					return
				}
				m.recordDiagForGeneration(generation, id, Diagnostic{
					Level: DiagWarn,
					Code:  CodeNoData,
					Kind:  DiagKindProvider,
					Title: "No market data",
					Detail: "Connected and the configured symbols are valid, " +
						"but no quotes are arriving.",
					Remediation: "This is not a configuration problem. Try Restart feeds; " +
						"if it persists it is a provider or system issue - contact support.",
					Actions: []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
				})
				return
			}
			for _, finding := range findings {
				m.recordDiagForGeneration(generation, id, finding)
			}
			return
		}
		m.recordDiagForGeneration(generation, id, Diagnostic{
			Level:       DiagWarn,
			Code:        CodeNoData,
			Kind:        DiagKindProvider,
			Title:       "No market data",
			Detail:      "No quotes are arriving and this provider offers no self-diagnosis.",
			Remediation: "Try Restart feeds; if it persists, contact support.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}},
		})
	}()
}

func (m *Manager) nextManualTokenLocked() uint64 {
	m.nextManualToken++
	return m.nextManualToken
}

func (m *Manager) startupManualActionsLocked(
	generation uint64,
) []startupManualAction {
	clearActions := make([]startupManualAction, 0)
	for identity, acknowledged := range m.manualMayBeLive {
		marks := m.manualMarks[identity.instanceID]
		desiredMark, desired := marks[identity.pair]
		token := m.manualTokens[identity.instanceID][identity.pair]
		if !desired {
			token = m.nextManualTokenLocked()
		}
		desiredSynthetic := m.manualSynthetic[identity.instanceID][identity.pair]
		state := manualClearState{
			generation: generation,
			token:      token,
			external:   acknowledged.external,
		}
		switch {
		case !desired || desiredMark == "":
			state.direct = true
			state.synthetic = acknowledged.synthetic
		case acknowledged.synthetic && !desiredSynthetic:
			state.synthetic = true
		default:
			continue
		}
		m.setPendingManualClearLocked(identity, state)
		clearActions = append(clearActions, startupManualAction{
			instanceID: identity.instanceID,
			update:     manualClearUpdate(identity.pair, state),
		})
	}

	pushActions := make([]startupManualAction, 0)
	for instanceID, marks := range m.manualMarks {
		pushable := m.byInstance[instanceID]
		externals := externalSymbolsFor(m.appliedConfig[instanceID].Subscriptions)
		for pair, mark := range marks {
			if mark == "" || pushable == nil {
				continue
			}
			identity := manualQuoteIdentity{instanceID: instanceID, pair: pair}
			mayBeLive := m.manualMayBeLive[identity]
			mayBeLive.mark = mark
			mayBeLive.synthetic = mayBeLive.synthetic || m.manualSynthetic[instanceID][pair]
			mayBeLive.external = externals[pair]
			m.manualMayBeLive[identity] = mayBeLive
			pushActions = append(pushActions, startupManualAction{
				instanceID: instanceID,
				external:   externals[pair],
				pushable:   pushable,
				update: manualQuoteForState(
					pair, mark, generation, m.manualTokens[instanceID][pair],
				),
			})
		}
	}
	return append(clearActions, pushActions...)
}

func (m *Manager) runStartupManualActions(
	ctx context.Context,
	runWG *sync.WaitGroup,
	generation uint64,
	sinkBarrier chan struct{},
	actions []startupManualAction,
) {
	defer runWG.Done()
	if err := m.acquireGate(ctx, m.manualPublisherGate); err != nil {
		return
	}
	defer m.releaseGate(m.manualPublisherGate)
	hadClear := false
	for _, action := range actions {
		if ctx.Err() != nil {
			return
		}
		if !action.update.clear {
			continue
		}
		hadClear = true
		err := m.applyManualClear(ctx, action.instanceID, action.update)
		if err != nil {
			m.recordManualReconciliationPending(
				generation, action.instanceID, action.external, err,
			)
		}
	}
	if !hadClear {
		if err := m.acquireGate(ctx, m.sinkApplyGate); err != nil {
			return
		}
		m.releaseGate(m.sinkApplyGate)
	}
	m.releaseSinkBarrierIfReady(generation, sinkBarrier)
	for _, action := range actions {
		if ctx.Err() != nil {
			return
		}
		if action.update.clear {
			continue
		}
		if err := m.pushManualBounded(ctx, action.pushable, action.update); err != nil {
			m.recordManualReconciliationPending(
				generation, action.instanceID, action.external, err,
			)
		}
	}
}

// reporterFor builds a per-instance StatusReporter that writes the connector's
// runtime connection state into m.statuses under mu. The connector invokes it
// from its background goroutine, so it must take mu itself. A callback for an
// older run is dropped even when a timed-out Stop has already started a new run.
func (m *Manager) reporterFor(instanceID string, generation uint64) StatusReporter {
	return func(ok bool, errMsg string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.started || m.manualGeneration != generation || m.statuses == nil {
			return
		}
		status := m.statuses[instanceID]
		if ok {
			status.State = StateOK
			status.Error = ""
			m.statuses[instanceID] = status
			return
		}
		status.State = StateError
		status.Error = errMsg
		m.statuses[instanceID] = status
		m.recordDiagLocked(instanceID, Diagnostic{
			Level:       DiagError,
			Code:        CodeConnectionError,
			Kind:        DiagKindEnvironment,
			Title:       "Connection error",
			Detail:      errMsg,
			Remediation: "The source may be unreachable or geo-blocked. Check network/region, then Restart feeds.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}, {Type: ActionOpenDocs}},
		})
	}
}

func (m *Manager) recordDiagForGeneration(
	generation uint64, instanceID string, diag Diagnostic,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.manualGeneration != generation {
		return
	}
	m.recordDiagLocked(instanceID, diag)
}

// recordDiagLocked appends a diagnostic for instanceID and emits one slog
// record at the matching level (Routine→Debug; DiagError→Error; DiagWarn→Warn;
// everything else→Info). Callers must hold mu. A nil m.statuses (no live run)
// drops the write. At is set to time.Now().UTC() when zero. Consecutive
// identical (Code, Instrument, Detail) entries are deduped (refreshing At
// only) so reconnect/backoff loops cannot flood the buffer or the log; only
// the most recent diagBufferCap entries are kept.
func (m *Manager) recordDiagLocked(instanceID string, diag Diagnostic) {
	if m.statuses == nil {
		return
	}
	if diag.At.IsZero() {
		diag.At = time.Now().UTC()
	}
	status := m.statuses[instanceID]
	if n := len(status.Diagnostics); n > 0 {
		last := &status.Diagnostics[n-1]
		if last.Code == diag.Code &&
			last.Instrument == diag.Instrument &&
			last.Detail == diag.Detail {
			last.At = diag.At
			m.statuses[instanceID] = status
			return
		}
	}
	// Emit exactly one log record per new (non-deduped) diagnostic.
	attrs := []any{"instance", instanceID, "code", diag.Code}
	if diag.Instrument != "" {
		attrs = append(attrs, "instrument", diag.Instrument)
	}
	if diag.Detail != "" {
		attrs = append(attrs, "detail", diag.Detail)
	}
	switch {
	case diag.Routine:
		m.logger.Debug(diag.Title, attrs...)
	case diag.Level == DiagError:
		m.logger.Error(diag.Title, attrs...)
	case diag.Level == DiagWarn:
		m.logger.Warn(diag.Title, attrs...)
	default:
		m.logger.Info(diag.Title, attrs...)
	}
	status.Diagnostics = append(status.Diagnostics, diag)
	if len(status.Diagnostics) > diagBufferCap {
		status.Diagnostics = status.Diagnostics[len(status.Diagnostics)-diagBufferCap:]
	}
	m.statuses[instanceID] = status
}

// drain forwards every quote from ch into the sink. Errors are recorded as
// diagnostics (and thus logged via recordDiag) without stopping the loop: one
// bad quote must not tear down a feed. It records each delivered instrument key
// into received so the watchdog goroutine can check coverage. It ends when ch
// closes.
func (m *Manager) drain(
	ctx context.Context,
	runWG *sync.WaitGroup,
	generation uint64,
	instance domain.ExternalID,
	symbols map[quoteInstrumentKey]string,
	syntheticPairs map[quoteInstrumentKey]struct{},
	ch <-chan QuoteUpdate,
	received *sync.Map,
	sinkBarrier <-chan struct{},
) {
	defer runWG.Done()
	instanceID := instance.String()
	for update := range ch {
		if !waitForSinkBarrier(ctx, sinkBarrier) {
			continue
		}
		if update.clear {
			err := m.applyManualClear(ctx, instanceID, update)
			if update.clearDone != nil {
				update.clearDone <- err
				close(update.clearDone)
			}
			continue
		}
		if err := m.acquireGate(ctx, m.sinkApplyGate); err != nil {
			continue
		}
		if !m.quoteUpdateIsCurrent(generation, instanceID, update) {
			m.releaseGate(m.sinkApplyGate)
			continue
		}
		received.Store(quoteInstrumentKey{base: update.Base, quote: update.Quote}, true)
		external := symbols[quoteInstrumentKey{base: update.Base, quote: update.Quote}]
		m.recordQuoteArrival(generation, instanceID, external, time.Now())
		if !m.runGenerationIsCurrent(generation) {
			m.releaseGate(m.sinkApplyGate)
			continue
		}
		sink := m.currentSink()
		if sink == nil {
			m.recordDiagForGeneration(generation, instanceID, Diagnostic{
				Level:       DiagWarn,
				Code:        CodeInternalError,
				Kind:        DiagKindProvider,
				Title:       "Failed to push quote to engine",
				Detail:      ErrNilSink.Error(),
				Remediation: "Usually transient; if persistent, Restart feeds or contact support.",
				Actions:     []DiagnosticAction{{Type: ActionRestart}},
			})
		} else {
			directApplied := m.pushToSink(generation, instanceID, sink, update)
			syntheticApplied := false
			if directApplied {
				m.recordAcceptedSnapshot(
					instanceID,
					update,
					quoteSnapshot(instance, symbols, update),
				)
				if _, planned := syntheticPairs[quoteInstrumentKey{
					base: update.Base, quote: update.Quote,
				}]; planned {
					if inverted, ok := InvertQuote(update); ok {
						syntheticApplied = m.pushToSink(
							generation, instanceID, sink, inverted,
						)
					}
				}
			}
			if update.manual && directApplied {
				m.acknowledgeManualQuote(
					generation, instanceID, external, update, syntheticApplied,
				)
			}
		}
		m.releaseGate(m.sinkApplyGate)
	}
}

func waitForSinkBarrier(ctx context.Context, barrier <-chan struct{}) bool {
	select {
	case <-ctx.Done():
		return false
	case <-barrier:
		return true
	}
}

func (m *Manager) quoteUpdateIsCurrent(
	generation uint64, instanceID string, update QuoteUpdate,
) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.manualGeneration != generation {
		return false
	}
	if update.manual {
		return m.manualUpdateIsCurrentLocked(instanceID, update)
	}
	applied, ok := m.appliedConfig[instanceID]
	if !ok || applied.Provider != domain.MarketDataProviderBYO {
		return true
	}
	mark, ok := m.manualMarks[instanceID][quoteInstrumentKey{
		base: update.Base, quote: update.Quote,
	}]
	return ok && mark != "" && mark == update.Mark
}

func (m *Manager) runGenerationIsCurrent(generation uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.started && m.manualGeneration == generation
}

func (m *Manager) manualUpdateIsCurrentLocked(
	instanceID string, update QuoteUpdate,
) bool {
	if !m.started || update.manualGeneration != m.manualGeneration {
		return false
	}
	key := quoteInstrumentKey{base: update.Base, quote: update.Quote}
	return m.manualTokens[instanceID][key] == update.manualToken &&
		m.manualMarks[instanceID][key] == update.Mark && update.Mark != ""
}

func (m *Manager) applyManualClear(
	ctx context.Context, instanceID string, update QuoteUpdate,
) error {
	if err := m.acquireGate(ctx, m.sinkApplyGate); err != nil {
		return err
	}
	defer m.releaseGate(m.sinkApplyGate)
	if !m.manualClearIsCurrent(instanceID, update) {
		return nil
	}
	sink := m.currentSink()
	if sink == nil {
		return ErrNilSink
	}
	clearer, ok := sink.(QuoteClearer)
	if !ok {
		return fmt.Errorf("marketdata: sink does not support quote clear")
	}
	if update.clearDirect {
		if err := clearer.Clear(update.Base, update.Quote); err != nil {
			if m.retireUnknownManualQuote(instanceID, update, err) {
				return nil
			}
			return fmt.Errorf(
				"marketdata: clear manual quote %d/%d for %s: %w",
				update.Base, update.Quote, instanceID, err,
			)
		}
	}
	if update.clearSynthetic {
		if err := clearer.Clear(update.Quote, update.Base); err != nil {
			if m.retireUnknownManualQuote(instanceID, update, err) {
				return nil
			}
			return fmt.Errorf(
				"marketdata: clear synthetic manual quote %d/%d for %s: %w",
				update.Quote, update.Base, instanceID, err,
			)
		}
	}
	m.completeManualClear(instanceID, update)
	return nil
}

func (m *Manager) manualClearIsCurrent(instanceID string, update QuoteUpdate) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || update.manualGeneration != m.manualGeneration {
		return false
	}
	identity := manualQuoteIdentity{
		instanceID: instanceID,
		pair:       quoteInstrumentKey{base: update.Base, quote: update.Quote},
	}
	state, ok := m.pendingManualClears[identity]
	return ok && state.generation == update.manualGeneration &&
		state.token == update.manualToken && state.direct == update.clearDirect &&
		state.synthetic == update.clearSynthetic
}

func (m *Manager) acknowledgeManualQuote(
	generation uint64,
	instanceID string,
	external string,
	update QuoteUpdate,
	syntheticApplied bool,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.manualGeneration != generation ||
		!m.manualUpdateIsCurrentLocked(instanceID, update) {
		return
	}
	identity := manualQuoteIdentity{
		instanceID: instanceID,
		pair:       quoteInstrumentKey{base: update.Base, quote: update.Quote},
	}
	acknowledged := m.manualAcknowledged[identity]
	acknowledged.mark = update.Mark
	acknowledged.external = external
	if syntheticApplied {
		acknowledged.synthetic = true
	}
	m.manualAcknowledged[identity] = acknowledged
	mayBeLive := m.manualMayBeLive[identity]
	mayBeLive.mark = update.Mark
	mayBeLive.external = external
	if syntheticApplied {
		mayBeLive.synthetic = true
	}
	m.manualMayBeLive[identity] = mayBeLive
}

func (m *Manager) pushToSink(
	generation uint64, instanceID string, sink Sink, update QuoteUpdate,
) bool {
	if err := sink.Push(update); err != nil {
		m.recordDiagForGeneration(generation, instanceID, Diagnostic{
			Level:       DiagWarn,
			Code:        CodeInternalError,
			Kind:        DiagKindProvider,
			Title:       "Failed to push quote to engine",
			Detail:      err.Error(),
			Remediation: "Usually transient; if persistent, Restart feeds or contact support.",
			Actions:     []DiagnosticAction{{Type: ActionRestart}},
		})
		return false
	}
	return true
}

func quoteSnapshot(
	instance domain.ExternalID, symbols map[quoteInstrumentKey]string, update QuoteUpdate,
) domain.MarketDataQuote {
	receivedAt := time.Now().UTC()
	asOf := update.AsOf
	if asOf.IsZero() {
		asOf = receivedAt
	}
	return domain.MarketDataQuote{
		AsOf:           asOf.UTC(),
		ReceivedAt:     receivedAt,
		Instance:       instance,
		ExternalSymbol: symbols[quoteInstrumentKey{base: update.Base, quote: update.Quote}],
		Mark:           update.Mark,
		Bid:            update.Bid,
		Ask:            update.Ask,
	}
}

// intervalKey is the per-(instance, external symbol) key for the interval maps.
func intervalKey(instanceID, external string) string {
	return instanceID + "\x00" + external
}

// recordQuoteArrival records the arrival time of one tick and, when a previous
// arrival is known for the same (instance, external) key, the gap between them.
// The first tick for a key records only the arrival, leaving the interval
// unknown until a second tick arrives. It confirms the run under mu before
// taking the dedicated intervalMu on the per-tick drain path.
func (m *Manager) recordQuoteArrival(
	generation uint64, instanceID, external string, arrival time.Time,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started || m.manualGeneration != generation {
		return
	}
	key := intervalKey(instanceID, external)
	m.intervalMu.Lock()
	defer m.intervalMu.Unlock()
	if m.lastArrival == nil {
		m.lastArrival = make(map[string]time.Time)
	}
	if m.lastInterval == nil {
		m.lastInterval = make(map[string]time.Duration)
	}
	if prev, ok := m.lastArrival[key]; ok {
		if gap := arrival.Sub(prev); gap > 0 {
			m.lastInterval[key] = gap
		}
	}
	m.lastArrival[key] = arrival
}

func (m *Manager) recordAcceptedSnapshot(
	instanceID string,
	update QuoteUpdate,
	quote domain.MarketDataQuote,
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// The caller holds sinkApplyGate from before Sink.Push until this write
	// completes. Record every accepted push even if Stop timed out and Start
	// advanced the generation while the sink was blocked; a later generation
	// cannot acquire the gate and overwrite this snapshot out of order.
	m.latestQuotes[quoteSnapshotIdentity{
		instanceID: instanceID,
		external:   quote.ExternalSymbol,
		pair: quoteInstrumentKey{
			base: update.Base, quote: update.Quote,
		},
	}] = quote
}

// QuoteUpdateInterval returns the elapsed time between the two most recent ticks
// of the identified instrument's quote, and whether that interval is known. It
// is unknown (ok=false) until at least two ticks have arrived since the last
// (re)subscribe. The value is fixed between ticks; it does not grow with age.
func (m *Manager) QuoteUpdateInterval(instanceID, external string) (time.Duration, bool) {
	m.intervalMu.Lock()
	defer m.intervalMu.Unlock()
	interval, ok := m.lastInterval[intervalKey(instanceID, external)]
	return interval, ok
}

// clearInstanceIntervals drops every interval entry belonging to instanceID so a
// restart of that instance starts from "unknown" again. Keys are prefixed with
// the instance id, so all of the instance's entries share the prefix.
func (m *Manager) clearInstanceIntervals(instanceID string) {
	prefix := instanceID + "\x00"
	m.intervalMu.Lock()
	defer m.intervalMu.Unlock()
	for key := range m.lastArrival {
		if strings.HasPrefix(key, prefix) {
			delete(m.lastArrival, key)
		}
	}
	for key := range m.lastInterval {
		if strings.HasPrefix(key, prefix) {
			delete(m.lastInterval, key)
		}
	}
}

// Stop cancels the run context, closes every connector so its channel ends, and
// waits for the drain goroutines to finish. It is idempotent: a second Stop (or
// a Stop before Start) is a no-op.
func (m *Manager) Stop() {
	m.mu.Lock()
	if !m.started {
		m.mu.Unlock()
		return
	}
	m.started = false
	stoppedGeneration := m.manualGeneration
	cancel := m.cancel
	connectors := m.connectors
	runWG := m.runWG
	stopGrace := m.stopGrace
	m.connectors = nil
	// Clear byInstance before Close so a concurrent PushManual cannot reach a
	// connector that is being shut down.
	m.byInstance = nil
	m.cancel = nil
	m.runWG = nil
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, connector := range connectors {
			connector.Close()
		}
		if runWG != nil {
			runWG.Wait()
		}
	}()
	select {
	case <-done:
	case <-time.After(stopGrace):
		m.logger.Warn("market data shutdown timed out", "timeout", stopGrace)
	}

	clearStoppedState := false
	m.mu.Lock()
	if !m.started && m.manualGeneration == stoppedGeneration {
		m.statuses = nil
		m.appliedConfig = nil
		m.syntheticPairs = nil
		m.manualMarks = nil
		m.manualRevisions = nil
		m.manualTokens = nil
		m.manualSynthetic = nil
		m.pendingManualClears = nil
		m.sinkBarrier = nil
		m.sinkBarrierPending = nil
		clearStoppedState = true
	}
	m.mu.Unlock()

	if clearStoppedState {
		m.intervalMu.Lock()
		m.lastArrival = nil
		m.lastInterval = nil
		m.intervalMu.Unlock()
	}
}

// UseSink replaces the quote sink used by the next Start. It must be called
// while the manager is stopped; changing the sink while drain goroutines are
// active would route one feed run into two engine instances.
func (m *Manager) UseSink(sink Sink) error {
	if sink == nil {
		return ErrNilSink
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return fmt.Errorf("marketdata: cannot replace sink while running")
	}
	m.sink = sink
	return nil
}

// UseSinkProvider installs a resolver that returns the current engine's sink on
// every push. Unlike UseSink it may be set once at construction and needs no
// restart on an engine rebuild: each node-scoped engine has a current resolver
// while its SDK market-data service remains shared across rebuilds.
func (m *Manager) UseSinkProvider(provider func() Sink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sinkProvider = provider
}

// currentSink returns the sink to push into: the live provider result when a
// provider is installed, otherwise the static sink. The provider is invoked
// without holding mu so it never nests the manager lock under the engine lock.
func (m *Manager) currentSink() Sink {
	m.mu.Lock()
	provider := m.sinkProvider
	static := m.sink
	m.mu.Unlock()
	if provider != nil {
		return provider()
	}
	return static
}

// InstanceStatuses returns a copy of the current per-instance runtime statuses.
func (m *Manager) InstanceStatuses() map[string]InstanceRuntimeStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]InstanceRuntimeStatus, len(m.statuses))
	for id, status := range m.statuses {
		if status.Diagnostics != nil {
			diags := make([]Diagnostic, len(status.Diagnostics))
			copy(diags, status.Diagnostics)
			status.Diagnostics = diags
		}
		out[id] = status
	}
	return out
}

// QuoteSnapshots returns the last successfully applied direct quote for each
// instance and external instrument.
func (m *Manager) QuoteSnapshots() []QuoteSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]QuoteSnapshot, 0, len(m.latestQuotes))
	for identity, quote := range m.latestQuotes {
		out = append(out, QuoteSnapshot{
			MarketDataQuote: quote,
			BaseAssetID:     identity.pair.base,
			QuoteAssetID:    identity.pair.quote,
		})
	}
	return out
}

// AppliedConfig returns a copy of the enabled config applied by the last Start.
func (m *Manager) AppliedConfig() map[string]AppliedInstanceConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]AppliedInstanceConfig, len(m.appliedConfig))
	for id, cfg := range m.appliedConfig {
		cfg.Subscriptions = cloneSubscriptions(cfg.Subscriptions)
		out[id] = cfg
	}
	return out
}

// Restart re-applies the configuration by stopping the running connectors and
// starting again from the current store state. It reuses the process-scoped
// base context captured by the first Start, never a request context, so the
// feeds outlive the request that triggered the restart. A Restart before any
// Start is a no-op. Stop and Start lock internally; Restart must not hold mu
// across them.
func (m *Manager) Restart() error {
	m.mu.Lock()
	baseCtx := m.baseCtx
	m.mu.Unlock()
	if baseCtx == nil {
		return nil
	}
	m.Stop()
	return m.Start(baseCtx)
}

// PushManual delivers an operator-set manual mark for one applied instrument
// into the identified running instance's connector as a single quote. It is
// the after-startup counterpart of the startup re-apply: the upsert path calls
// it once after persisting an instrument. A non-empty price is pushed normally;
// an empty price queues a clear through the same connector channel, ordering it
// after every older heartbeat. It is a no-op when the manager is not running,
// the connector is not push-capable, the instrument is disabled, or the applied
// subscription does not match. Clearing a quote identity that the engine can no
// longer resolve also retires the identity as a no-op and records a warning that
// a stale quote may remain. The topology cases leave reconciliation to the
// explicit manager Restart.
func (m *Manager) PushManual(
	ctx context.Context, instanceID string, instrument domain.MarketDataInstrument,
) error {
	if !instrument.Enabled {
		return nil
	}
	operationCtx, cancel := context.WithTimeout(ctx, m.stopGrace)
	defer cancel()
	m.mu.Lock()
	generation := m.manualGeneration
	m.mu.Unlock()
	if err := m.acquireGate(operationCtx, m.manualPublisherGate); err != nil {
		m.recordManualReconciliationPending(
			generation, instanceID, instrument.ExternalSymbol, err,
		)
		return fmt.Errorf("marketdata: manual publisher pending reconciliation: %w", err)
	}
	defer m.releaseGate(m.manualPublisherGate)

	m.mu.Lock()
	pushable := m.byInstance[instanceID]
	applied, appliedOK := m.appliedConfig[instanceID]
	if pushable == nil || !appliedOK || applied.Provider != domain.MarketDataProviderBYO ||
		!hasSubscription(applied.Subscriptions, instrument) {
		m.mu.Unlock()
		return nil
	}
	key := quoteInstrumentKey{base: instrument.BaseAssetID, quote: instrument.QuoteAssetID}
	marks := m.manualMarks[instanceID]
	if marks == nil {
		marks = make(map[quoteInstrumentKey]string)
		m.manualMarks[instanceID] = marks
	}
	m.manualRevisions[instanceID]++
	token := m.nextManualTokenLocked()
	marks[key] = instrument.ManualPrice
	tokens := m.manualTokens[instanceID]
	if tokens == nil {
		tokens = make(map[quoteInstrumentKey]uint64)
		m.manualTokens[instanceID] = tokens
	}
	tokens[key] = token
	generation = m.manualGeneration
	identity := manualQuoteIdentity{instanceID: instanceID, pair: key}
	synthetic := m.manualSynthetic[instanceID][key]
	clearState := manualClearState{}
	if instrument.ManualPrice == "" {
		mayBeLive := m.manualMayBeLive[identity]
		clearState = manualClearState{
			generation: generation,
			token:      token,
			direct:     true,
			synthetic:  mayBeLive.synthetic || synthetic,
			external:   instrument.ExternalSymbol,
		}
		m.setPendingManualClearLocked(identity, clearState)
	} else {
		m.supersedePendingManualClearLocked(identity, synthetic)
		mayBeLive := m.manualMayBeLive[identity]
		mayBeLive.mark = instrument.ManualPrice
		mayBeLive.synthetic = mayBeLive.synthetic || synthetic
		mayBeLive.external = instrument.ExternalSymbol
		m.manualMayBeLive[identity] = mayBeLive
	}
	m.mu.Unlock()

	if instrument.ManualPrice == "" {
		if err := m.pushManualClear(operationCtx, pushable, instrument, clearState); err != nil {
			m.recordManualReconciliationPending(
				generation, instanceID, instrument.ExternalSymbol, err,
			)
			return fmt.Errorf("marketdata: live manual clear pending reconciliation: %w", err)
		}
		return nil
	}
	if err := m.pushManualBounded(operationCtx, pushable, manualQuoteForState(
		key, instrument.ManualPrice, generation, token,
	)); err != nil {
		m.recordManualReconciliationPending(
			generation, instanceID, instrument.ExternalSymbol, err,
		)
		return fmt.Errorf("marketdata: live manual quote pending reconciliation: %w", err)
	}
	return nil
}

func (m *Manager) pushManualBounded(
	ctx context.Context, pushable Pushable, update QuoteUpdate,
) error {
	pushCtx, cancel := context.WithTimeout(ctx, m.stopGrace)
	defer cancel()
	return pushable.Push(pushCtx, update)
}

func (m *Manager) pushManualClear(
	ctx context.Context,
	pushable Pushable,
	instrument domain.MarketDataInstrument,
	state manualClearState,
) error {
	pushCtx, cancel := context.WithTimeout(ctx, m.stopGrace)
	defer cancel()
	done := make(chan error, 1)
	update := manualClearUpdate(quoteInstrumentKey{
		base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
	}, state)
	update.clearDone = done
	if err := pushable.Push(pushCtx, update); err != nil {
		return fmt.Errorf("enqueue manual clear: %w", err)
	}
	select {
	case err := <-done:
		return err
	case <-pushCtx.Done():
		return fmt.Errorf("wait for manual clear: %w", pushCtx.Err())
	}
}

func (m *Manager) completeManualClear(instanceID string, update QuoteUpdate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	identity := manualQuoteIdentity{
		instanceID: instanceID,
		pair:       quoteInstrumentKey{base: update.Base, quote: update.Quote},
	}
	state, ok := m.pendingManualClears[identity]
	if !ok || state.generation != update.manualGeneration ||
		state.token != update.manualToken {
		return
	}
	if update.clearDirect {
		delete(m.manualAcknowledged, identity)
		delete(m.manualMayBeLive, identity)
		delete(m.latestQuotes, quoteSnapshotIdentity{
			instanceID: instanceID,
			external:   state.external,
			pair: quoteInstrumentKey{
				base: update.Base, quote: update.Quote,
			},
		})
	} else if update.clearSynthetic {
		if acknowledged, ok := m.manualAcknowledged[identity]; ok {
			acknowledged.synthetic = false
			m.manualAcknowledged[identity] = acknowledged
		}
		if mayBeLive, ok := m.manualMayBeLive[identity]; ok {
			mayBeLive.synthetic = false
			m.manualMayBeLive[identity] = mayBeLive
		}
	}
	delete(m.pendingManualClears, identity)
	m.resolveSinkBarrierClearLocked(identity, state)
}

func (m *Manager) retireUnknownManualQuote(
	instanceID string, update QuoteUpdate, err error,
) bool {
	if !errors.Is(err, ErrUnknownAsset) {
		return false
	}
	identity := manualQuoteIdentity{
		instanceID: instanceID,
		pair: quoteInstrumentKey{
			base: update.Base, quote: update.Quote,
		},
	}
	instrument := fmt.Sprintf("%d/%d", update.Base, update.Quote)
	m.mu.Lock()
	if state, ok := m.pendingManualClears[identity]; ok && state.external != "" {
		instrument = state.external
	}
	m.retireManualQuoteLocked(identity)
	m.mu.Unlock()
	m.recordDiagForGeneration(update.manualGeneration, instanceID, Diagnostic{
		Level:      DiagWarn,
		Code:       CodeInternalError,
		Kind:       DiagKindConfig,
		Title:      "Manual quote identity retired",
		Instrument: instrument,
		Detail: fmt.Sprintf(
			"Quote identity %d/%d was retired because the engine can no longer "+
				"resolve its assets; a stale quote may remain: %v",
			update.Base, update.Quote, err,
		),
		Remediation: "Reconcile the engine asset registry before publishing " +
			"another manual quote.",
	})
	return true
}

func (m *Manager) retireManualQuoteLocked(identity manualQuoteIdentity) {
	if state, ok := m.pendingManualClears[identity]; ok {
		delete(m.pendingManualClears, identity)
		m.resolveSinkBarrierClearLocked(identity, state)
	}
	delete(m.manualAcknowledged, identity)
	delete(m.manualMayBeLive, identity)
	delete(m.manualMarks[identity.instanceID], identity.pair)
	delete(m.manualTokens[identity.instanceID], identity.pair)
	delete(m.manualSynthetic[identity.instanceID], identity.pair)
	for snapshotIdentity := range m.latestQuotes {
		if snapshotIdentity.instanceID == identity.instanceID &&
			snapshotIdentity.pair == identity.pair {
			delete(m.latestQuotes, snapshotIdentity)
		}
	}
}

func (m *Manager) setPendingManualClearLocked(
	identity manualQuoteIdentity, state manualClearState,
) {
	previous, hadPrevious := m.pendingManualClears[identity]
	if hadPrevious && previous.generation == m.sinkBarrierGeneration &&
		m.sinkBarrierPending[identity] == previous.token {
		m.sinkBarrierPending[identity] = state.token
	}
	m.pendingManualClears[identity] = state
}

func (m *Manager) supersedePendingManualClearLocked(
	identity manualQuoteIdentity, currentSynthetic bool,
) {
	state, ok := m.pendingManualClears[identity]
	if !ok {
		return
	}
	if state.synthetic && !currentSynthetic {
		state.direct = false
		m.pendingManualClears[identity] = state
		return
	}
	delete(m.pendingManualClears, identity)
	m.resolveSinkBarrierClearLocked(identity, state)
}

func (m *Manager) resolveSinkBarrierClearLocked(
	identity manualQuoteIdentity, state manualClearState,
) {
	if m.sinkBarrier == nil || state.generation != m.sinkBarrierGeneration ||
		m.sinkBarrierPending[identity] != state.token {
		return
	}
	delete(m.sinkBarrierPending, identity)
	if len(m.sinkBarrierPending) == 0 {
		close(m.sinkBarrier)
		m.sinkBarrier = nil
		m.sinkBarrierPending = nil
	}
}

func (m *Manager) releaseSinkBarrierIfReady(
	generation uint64, barrier chan struct{},
) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sinkBarrier != barrier || generation != m.sinkBarrierGeneration ||
		len(m.sinkBarrierPending) != 0 {
		return
	}
	close(m.sinkBarrier)
	m.sinkBarrier = nil
	m.sinkBarrierPending = nil
}

func (m *Manager) acquireGate(ctx context.Context, gate chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gate:
		if err := ctx.Err(); err != nil {
			gate <- struct{}{}
			return err
		}
		return nil
	}
}

func (*Manager) releaseGate(gate chan struct{}) {
	gate <- struct{}{}
}

func manualClearUpdate(pair quoteInstrumentKey, state manualClearState) QuoteUpdate {
	return QuoteUpdate{
		Base:             pair.base,
		Quote:            pair.quote,
		clear:            true,
		clearDirect:      state.direct,
		clearSynthetic:   state.synthetic,
		manual:           true,
		manualGeneration: state.generation,
		manualToken:      state.token,
	}
}

func manualQuoteForState(
	pair quoteInstrumentKey, mark string, generation, token uint64,
) QuoteUpdate {
	return QuoteUpdate{
		Base:             pair.base,
		Quote:            pair.quote,
		Mark:             mark,
		manual:           true,
		manualGeneration: generation,
		manualToken:      token,
	}
}

func (m *Manager) recordManualReconciliationPending(
	generation uint64, instanceID, instrument string, err error,
) {
	m.recordDiagForGeneration(generation, instanceID, Diagnostic{
		Level:       DiagWarn,
		Code:        CodeInternalError,
		Kind:        DiagKindProvider,
		Title:       "Manual quote reconciliation pending",
		Detail:      err.Error(),
		Remediation: "The saved configuration will be retried automatically.",
		Instrument:  instrument,
	})
}

// refreshManualMarks periodically rereads current BYO configuration and sends
// every enabled non-empty mark through its running connector. The connector's
// normal drain path stamps a fresh AsOf, records the accepted direct snapshot,
// then updates both direct and planned synthetic engine pairs. A live edit or
// clear is observed on the next tick.
func (m *Manager) refreshManualMarks(
	ctx context.Context, runWG *sync.WaitGroup, interval time.Duration,
) {
	defer runWG.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.refreshManualMarksOnce(ctx)
		}
	}
}

func (m *Manager) refreshManualMarksOnce(ctx context.Context) {
	if err := m.acquireGate(ctx, m.manualPublisherGate); err != nil {
		return
	}
	m.retryPendingManualClears(ctx)
	m.releaseGate(m.manualPublisherGate)

	m.mu.Lock()
	instanceIDs := make([]string, 0, len(m.byInstance))
	for instanceID := range m.byInstance {
		instanceIDs = append(instanceIDs, instanceID)
	}
	m.mu.Unlock()

	for _, instanceID := range instanceIDs {
		if ctx.Err() != nil {
			return
		}
		m.mu.Lock()
		revision := m.manualRevisions[instanceID]
		generation := m.manualGeneration
		m.mu.Unlock()
		instance, err := domain.ParseExternalID(instanceID)
		if err != nil {
			continue
		}
		instruments, err := m.store.ListEnabledMarketDataInstruments(ctx, instance)
		if err != nil {
			m.recordDiagForGeneration(generation, instanceID, Diagnostic{
				Level:       DiagWarn,
				Code:        CodeInternalError,
				Kind:        DiagKindProvider,
				Title:       "Failed to refresh manual marks",
				Detail:      err.Error(),
				Remediation: "Usually transient; if persistent, Restart feeds or contact support.",
				Actions:     []DiagnosticAction{{Type: ActionRestart}},
			})
			continue
		}

		if err := m.acquireGate(ctx, m.manualPublisherGate); err != nil {
			return
		}
		m.mu.Lock()
		pushable := m.byInstance[instanceID]
		applied, appliedOK := m.appliedConfig[instanceID]
		if generation != m.manualGeneration || revision != m.manualRevisions[instanceID] {
			m.mu.Unlock()
			m.releaseGate(m.manualPublisherGate)
			continue
		}
		if pushable == nil || !appliedOK ||
			applied.Provider != domain.MarketDataProviderBYO {
			m.mu.Unlock()
			m.releaseGate(m.manualPublisherGate)
			continue
		}
		marks := m.manualMarks[instanceID]
		if marks == nil {
			marks = make(map[quoteInstrumentKey]string)
			m.manualMarks[instanceID] = marks
		}
		tokens := m.manualTokens[instanceID]
		if tokens == nil {
			tokens = make(map[quoteInstrumentKey]uint64)
			m.manualTokens[instanceID] = tokens
		}
		type manualAction struct {
			instrument domain.MarketDataInstrument
			state      manualClearState
			update     QuoteUpdate
		}
		actions := make([]manualAction, 0, len(instruments))
		for _, instrument := range instruments {
			if !hasSubscription(applied.Subscriptions, instrument) {
				continue
			}
			key := quoteInstrumentKey{
				base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
			}
			identity := manualQuoteIdentity{instanceID: instanceID, pair: key}
			if instrument.ManualPrice == "" {
				mayBeLive, mayBeLiveOK := m.manualMayBeLive[identity]
				pending, pendingOK := m.pendingManualClears[identity]
				marks[key] = ""
				if mayBeLiveOK || pendingOK {
					token := m.nextManualTokenLocked()
					tokens[key] = token
					state := manualClearState{
						generation: generation,
						token:      token,
						direct:     true,
						synthetic: mayBeLive.synthetic || pending.synthetic ||
							m.manualSynthetic[instanceID][key],
						external: instrument.ExternalSymbol,
					}
					m.setPendingManualClearLocked(identity, state)
					actions = append(actions, manualAction{
						instrument: instrument,
						state:      state,
					})
				}
				continue
			}
			if marks[key] != instrument.ManualPrice || tokens[key] == 0 {
				tokens[key] = m.nextManualTokenLocked()
			}
			marks[key] = instrument.ManualPrice
			m.supersedePendingManualClearLocked(
				identity, m.manualSynthetic[instanceID][key],
			)
			mayBeLive := m.manualMayBeLive[identity]
			mayBeLive.mark = instrument.ManualPrice
			mayBeLive.synthetic = mayBeLive.synthetic ||
				m.manualSynthetic[instanceID][key]
			mayBeLive.external = instrument.ExternalSymbol
			m.manualMayBeLive[identity] = mayBeLive
			actions = append(actions, manualAction{
				instrument: instrument,
				update: manualQuoteForState(
					key, instrument.ManualPrice, generation, tokens[key],
				),
			})
		}
		m.mu.Unlock()
		for _, action := range actions {
			if ctx.Err() != nil {
				break
			}
			var pushErr error
			if action.instrument.ManualPrice == "" {
				pushErr = m.pushManualClear(ctx, pushable, action.instrument, action.state)
			} else {
				pushErr = m.pushManualBounded(ctx, pushable, action.update)
			}
			if pushErr != nil {
				m.recordManualReconciliationPending(
					generation, instanceID, action.instrument.ExternalSymbol, pushErr,
				)
			}
		}
		m.releaseGate(m.manualPublisherGate)
	}
}

func (m *Manager) retryPendingManualClears(ctx context.Context) {
	type pendingClear struct {
		identity manualQuoteIdentity
		state    manualClearState
	}
	m.mu.Lock()
	pending := make([]pendingClear, 0, len(m.pendingManualClears))
	for identity, state := range m.pendingManualClears {
		if state.generation != m.manualGeneration {
			continue
		}
		pending = append(pending, pendingClear{identity: identity, state: state})
	}
	m.mu.Unlock()
	for _, clear := range pending {
		if ctx.Err() != nil {
			return
		}
		if err := m.applyManualClear(
			ctx,
			clear.identity.instanceID,
			manualClearUpdate(clear.identity.pair, clear.state),
		); err != nil {
			m.recordManualReconciliationPending(
				clear.state.generation, clear.identity.instanceID,
				fmt.Sprintf(
					"%d/%d", clear.identity.pair.base, clear.identity.pair.quote,
				),
				err,
			)
		}
	}
}

// manualQuote builds the one-shot quote carrying an instrument's operator-set
// manual mark. AsOf is left zero so the sink publishes it as current and the
// manager snapshot stamps it with receipt time, matching a fresh push.
func manualQuote(instrument domain.MarketDataInstrument) QuoteUpdate {
	return QuoteUpdate{
		Base:  instrument.BaseAssetID,
		Quote: instrument.QuoteAssetID,
		Mark:  instrument.ManualPrice,
	}
}

// subscriptionsFor maps enabled instrument rows to subscriptions: external
// symbol plus the instrument (base, quote) it resolves to.
func subscriptionsFor(
	instruments []domain.MarketDataInstrument,
	syntheticPairs map[quoteInstrumentKey]struct{},
) []Subscription {
	subs := make([]Subscription, 0, len(instruments))
	for _, instrument := range instruments {
		_, syntheticInverse := syntheticPairs[quoteInstrumentKey{
			base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
		}]
		subs = append(subs, Subscription{
			External:         instrument.ExternalSymbol,
			Base:             instrument.BaseAssetID,
			Quote:            instrument.QuoteAssetID,
			SyntheticInverse: syntheticInverse,
		})
	}
	return subs
}

func (m *Manager) syntheticPairPlan(
	ctx context.Context,
) (map[quoteInstrumentKey]struct{}, error) {
	instances, err := m.store.ListMarketDataInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("marketdata: list configured instances: %w", err)
	}
	pairs := make(map[quoteInstrumentKey]struct{})
	for _, instance := range instances {
		instruments, err := m.store.ListMarketDataInstruments(ctx, instance.ExternalID)
		if err != nil {
			return nil, fmt.Errorf(
				"marketdata: list configured instruments for %s: %w",
				instance.ExternalID, err,
			)
		}
		for _, instrument := range instruments {
			pairs[quoteInstrumentKey{
				base: instrument.BaseAssetID, quote: instrument.QuoteAssetID,
			}] = struct{}{}
		}
	}
	plan := make(map[quoteInstrumentKey]struct{}, len(pairs))
	for pair := range pairs {
		if _, configured := pairs[quoteInstrumentKey{
			base: pair.quote, quote: pair.base,
		}]; !configured {
			plan[pair] = struct{}{}
		}
	}
	return plan, nil
}

func cloneSubscriptions(subs []Subscription) []Subscription {
	if len(subs) == 0 {
		return nil
	}
	out := make([]Subscription, len(subs))
	copy(out, subs)
	return out
}

type quoteInstrumentKey struct {
	base  domain.EngineAssetID
	quote domain.EngineAssetID
}

type quoteSnapshotIdentity struct {
	instanceID string
	external   string
	pair       quoteInstrumentKey
}

func externalSymbolsFor(subs []Subscription) map[quoteInstrumentKey]string {
	symbols := make(map[quoteInstrumentKey]string, len(subs))
	for _, sub := range subs {
		symbols[quoteInstrumentKey{base: sub.Base, quote: sub.Quote}] = sub.External
	}
	return symbols
}

func hasSubscription(subs []Subscription, instrument domain.MarketDataInstrument) bool {
	for _, sub := range subs {
		if sub.External == instrument.ExternalSymbol &&
			sub.Base == instrument.BaseAssetID &&
			sub.Quote == instrument.QuoteAssetID {
			return true
		}
	}
	return false
}

// discard is an io.Writer that drops everything, backing the manager's default
// no-op logger when the caller supplies none.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
