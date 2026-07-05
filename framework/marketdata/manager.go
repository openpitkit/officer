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

// Store is the read seam the manager needs: the enabled instances and, per
// instance, its enabled instruments. It is the subset of the officer Store the
// runtime consumes; the concrete sqlite store satisfies it.
type Store interface {
	// ListEnabledMarketDataInstances returns every enabled market-data instance.
	ListEnabledMarketDataInstances(ctx context.Context) ([]domain.MarketDataInstance, error)
	// ListEnabledMarketDataInstruments returns the enabled instruments of the
	// identified instance.
	ListEnabledMarketDataInstruments(
		ctx context.Context, instance domain.ExternalID,
	) ([]domain.MarketDataInstrument, error)
	// UpsertMarketDataQuote records the latest quote snapshot for panel and
	// dashboard observability.
	UpsertMarketDataQuote(ctx context.Context, quote domain.MarketDataQuote) error
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
	// sinkProvider, when set, resolves the live engine sink on every push. The
	// engine sink is recreated on each engine rebuild (account/group/asset
	// changes), so a cached sink goes stale ("market-data service is null") until
	// a restart. Resolving per push keeps quotes flowing across rebuilds without a
	// restart. It takes precedence over the static sink.
	sinkProvider func() Sink
	logger       *slog.Logger

	mu            sync.Mutex
	baseCtx       context.Context
	started       bool
	cancel        context.CancelFunc
	runWG         *sync.WaitGroup
	connectors    []Connector
	appliedConfig map[string]AppliedInstanceConfig
	// byInstance maps an instance id to its live connector, so a manual quote
	// can be pushed into a specific running instance's connector after startup.
	// Only push-capable (Pushable) connectors are entered.
	byInstance map[string]Connector
	statuses   map[string]InstanceRuntimeStatus

	// intervalMu guards the inter-update interval state below. It is a dedicated
	// lock, not mu, so the per-tick update on the drain hot path never contends
	// with status reads under mu. Keyed by intervalKey(instanceID, external).
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
		registry:  registry,
		store:     store,
		sink:      sink,
		stopGrace: defaultManagerStopGrace,
		logger:    logger,
	}, nil
}

// Registry returns the connector registry used by this manager.
func (m *Manager) Registry() *Registry {
	return m.registry
}

// Start reads the enabled instances and brings up one connector per instance.
// It is safe to call once; a second Start while running is a no-op. An instance
// whose type is unknown, whose instruments cannot be read, or whose Subscribe
// fails is logged and skipped - it never fails Start or stops the other
// instances. With nothing enabled Start is a clean no-op.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return nil
	}
	if m.sink == nil {
		return ErrNilSink
	}
	m.baseCtx = ctx

	instances, err := m.store.ListEnabledMarketDataInstances(ctx)
	if err != nil {
		return fmt.Errorf("marketdata: list enabled instances: %w", err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	runWG := &sync.WaitGroup{}
	m.cancel = cancel
	m.runWG = runWG
	m.started = true
	m.statuses = make(map[string]InstanceRuntimeStatus, len(instances))
	m.byInstance = make(map[string]Connector, len(instances))
	m.appliedConfig = make(map[string]AppliedInstanceConfig, len(instances))

	m.intervalMu.Lock()
	m.lastArrival = make(map[string]time.Time)
	m.lastInterval = make(map[string]time.Duration)
	m.intervalMu.Unlock()

	for _, instance := range instances {
		m.startInstanceLocked(runCtx, runWG, instance)
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
	instance domain.MarketDataInstance,
) {
	// Use the 22-char string form of the external id as the runtime map key.
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
		sr.SetStatusReporter(m.reporterFor(instanceID))
	}
	if dr, ok := connector.(DiagnosticReporting); ok {
		id := instanceID
		dr.SetDiagnosticReporter(func(diag Diagnostic) {
			m.recordDiag(id, diag)
		})
	}

	subs := subscriptionsFor(instruments)
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

	// Build the set of expected instrument keys for the watchdog.
	expectedKeys := make([]string, 0, len(instruments))
	for _, inst := range instruments {
		expectedKeys = append(expectedKeys, inst.BaseAsset+"\x00"+inst.QuoteAsset)
	}

	// received tracks which instrument keys have delivered at least one update.
	// sync.Map is used so drain can write and the watchdog can read concurrently
	// without additional locks.
	received := &sync.Map{}
	m.connectors = append(m.connectors, connector)
	runWG.Add(1)
	go m.drain(ctx, runWG, instanceID, symbols, ch, received)
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
	if pushableOK {
		m.byInstance[instanceID] = connector
		quotes := make([]QuoteUpdate, 0, len(instruments))
		for _, inst := range instruments {
			if inst.ManualPrice == "" {
				continue
			}
			quotes = append(quotes, manualQuote(inst))
		}
		if len(quotes) > 0 {
			runWG.Add(1)
			go func() {
				defer runWG.Done()
				for _, quote := range quotes {
					pushable.Push(quote)
				}
			}()
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
				m.recordDiag(id, Diagnostic{
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
				m.recordDiag(id, Diagnostic{
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
				m.recordDiag(id, finding)
			}
			return
		}
		m.recordDiag(id, Diagnostic{
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

// reporterFor builds a per-instance StatusReporter that writes the connector's
// runtime connection state into m.statuses under mu. The connector invokes it
// from its background goroutine, so it must take mu itself. A nil m.statuses
// (after Stop reset it) means there is no live run to report into, so the write
// is dropped.
func (m *Manager) reporterFor(instanceID string) StatusReporter {
	return func(ok bool, errMsg string) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.statuses == nil {
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

// recordDiag appends a structured diagnostic for instanceID, locking mu. It is
// the variant for callers that do NOT hold mu (drain goroutine, connector
// reporter, watchdog). recordDiagLocked holds the body.
func (m *Manager) recordDiag(instanceID string, diag Diagnostic) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	instanceID string,
	symbols map[quoteInstrumentKey]string,
	ch <-chan QuoteUpdate,
	received *sync.Map,
) {
	defer runWG.Done()
	for update := range ch {
		received.Store(update.Base+"\x00"+update.Quote, true)
		external := symbols[quoteInstrumentKey{base: update.Base, quote: update.Quote}]
		m.recordQuoteArrival(instanceID, external, time.Now())
		if err := m.store.UpsertMarketDataQuote(
			ctx, quoteSnapshot(instanceID, symbols, update),
		); err != nil {
			m.recordDiag(instanceID, Diagnostic{
				Level:       DiagWarn,
				Code:        CodeInternalError,
				Kind:        DiagKindProvider,
				Title:       "Failed to record quote",
				Detail:      err.Error(),
				Remediation: "Usually transient; if persistent, Restart feeds or contact support.",
				Actions:     []DiagnosticAction{{Type: ActionRestart}},
			})
		}
		sink := m.currentSink()
		if sink == nil {
			m.recordDiag(instanceID, Diagnostic{
				Level:       DiagWarn,
				Code:        CodeInternalError,
				Kind:        DiagKindProvider,
				Title:       "Failed to push quote to engine",
				Detail:      ErrNilSink.Error(),
				Remediation: "Usually transient; if persistent, Restart feeds or contact support.",
				Actions:     []DiagnosticAction{{Type: ActionRestart}},
			})
		} else if err := sink.Push(update); err != nil {
			m.recordDiag(instanceID, Diagnostic{
				Level:       DiagWarn,
				Code:        CodeInternalError,
				Kind:        DiagKindProvider,
				Title:       "Failed to push quote to engine",
				Detail:      err.Error(),
				Remediation: "Usually transient; if persistent, Restart feeds or contact support.",
				Actions:     []DiagnosticAction{{Type: ActionRestart}},
			})
		}
	}
}

func quoteSnapshot(
	instanceID string, symbols map[quoteInstrumentKey]string, update QuoteUpdate,
) domain.MarketDataQuote {
	receivedAt := time.Now().UTC()
	asOf := update.AsOf
	if asOf.IsZero() {
		asOf = receivedAt
	}
	// instanceID is the 22-char string form; parse it back to the typed handle.
	// The value was produced by ExternalID.String() so parsing always succeeds;
	// a parse failure yields a zero ExternalID which is the safe fallback.
	instance, _ := domain.ParseExternalID(instanceID)
	return domain.MarketDataQuote{
		AsOf:           asOf.UTC(),
		ReceivedAt:     receivedAt,
		Instance:       instance,
		ExternalSymbol: symbols[quoteInstrumentKey{base: update.Base, quote: update.Quote}],
		BaseAsset:      update.Base,
		QuoteAsset:     update.Quote,
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
// unknown until a second tick arrives. Called from drain on the per-tick hot
// path, so it locks the dedicated intervalMu, never mu.
func (m *Manager) recordQuoteArrival(instanceID, external string, arrival time.Time) {
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

	m.mu.Lock()
	m.statuses = nil
	m.appliedConfig = nil
	m.mu.Unlock()

	m.intervalMu.Lock()
	m.lastArrival = nil
	m.lastInterval = nil
	m.intervalMu.Unlock()
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

// UseSinkProvider installs a resolver that returns the current engine sink on
// every push. Unlike UseSink it may be set once at construction and needs no
// restart on an engine rebuild: the manager always pushes into the live engine,
// so a rebuild never leaves it pushing into a closed service.
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

// PushManual delivers an operator-set manual mark for one instrument into the
// identified running instance's connector as a single quote. It is the
// after-startup counterpart of the startup re-apply: the upsert path calls it
// once after persisting an instrument with a manual price. It is a no-op when
// the manager is not running, the instance is not running, the instance's
// connector is not push-capable, the instrument is disabled, or the price is
// empty - mirroring what the startup re-apply would (or would not) push, so a
// streaming provider is never affected. The push itself is non-blocking and
// drains through the connector's channel into the sink like any quote.
func (m *Manager) PushManual(instanceID string, instrument domain.MarketDataInstrument) {
	if !instrument.Enabled || instrument.ManualPrice == "" {
		return
	}
	m.mu.Lock()
	connector := m.byInstance[instanceID]
	m.mu.Unlock()
	if connector == nil {
		return
	}
	if pushable, ok := connector.(Pushable); ok {
		pushable.Push(manualQuote(instrument))
	}
}

// manualQuote builds the one-shot quote carrying an instrument's operator-set
// manual mark. AsOf is left zero so the sink/store stamps it with receipt time,
// matching a fresh push.
func manualQuote(instrument domain.MarketDataInstrument) QuoteUpdate {
	return QuoteUpdate{
		Base:  instrument.BaseAsset,
		Quote: instrument.QuoteAsset,
		Mark:  instrument.ManualPrice,
	}
}

// subscriptionsFor maps enabled instrument rows to subscriptions: external
// symbol plus the instrument (base, quote) it resolves to.
func subscriptionsFor(instruments []domain.MarketDataInstrument) []Subscription {
	subs := make([]Subscription, 0, len(instruments))
	for _, instrument := range instruments {
		subs = append(subs, Subscription{
			External: instrument.ExternalSymbol,
			Base:     instrument.BaseAsset,
			Quote:    instrument.QuoteAsset,
		})
	}
	return subs
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
	base  string
	quote string
}

func externalSymbolsFor(subs []Subscription) map[quoteInstrumentKey]string {
	symbols := make(map[quoteInstrumentKey]string, len(subs))
	for _, sub := range subs {
		symbols[quoteInstrumentKey{base: sub.Base, quote: sub.Quote}] = sub.External
	}
	return symbols
}

// discard is an io.Writer that drops everything, backing the manager's default
// no-op logger when the caller supplies none.
type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }
