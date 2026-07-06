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

package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"go.openpit.dev/openpit/pretrade"

	"go.openpit.dev/officer/framework/domain"
	fwengine "go.openpit.dev/officer/framework/engine"
)

// defaultHoldTTL is how long a held reservation lives before the TTL sweeper
// auto-rolls it back. A held reservation holds no engine/storage lock, so this
// multi-minute window blocks nothing.
const defaultHoldTTL = 120 * time.Second

// sweepInterval is the TTL sweeper tick.
const sweepInterval = time.Second

// resolvedRetention bounds how long explicit terminal ids stay in memory for
// duplicate resolve detection.
const resolvedRetention = defaultHoldTTL

// reservationState is the lifecycle state of a registry entry. Held is the only
// resolvable state; Resolving is the transient single-resolve guard set before
// the native commit/rollback; Committed and RolledBack are terminal.
type reservationState int

const (
	reservationStateHeld reservationState = iota
	reservationStateResolving
	reservationStateCommitted
	reservationStateRolledBack
)

type resolvedReservation struct {
	expiresAt time.Time
	state     reservationState
}

// terminal reports whether the state is a final resolution.
func (s reservationState) terminal() bool {
	return s == reservationStateCommitted || s == reservationStateRolledBack
}

// heldReservation is one live held pre-trade reservation tracked by the
// registry. The native handle is kept outside e.mu and is only touched under
// e.mu during resolution; the state field is guarded by registry.mu.
type heldReservation struct {
	res        *pretrade.Reservation
	issuedAt   time.Time
	expiresAt  time.Time
	account    domain.AccountID
	params     domain.Order
	approvalID string
	// lock is the SDK-serialized reservation lock captured at hold time, ready to
	// persist verbatim on the order and reservation intent. Nil when the order
	// locked nothing.
	lock []byte
	// settlement is the settlement-leg lock price as a decimal string (display
	// only); source is how it was derived (limit vs market mark).
	settlement string
	source     string
	outcomes   []BalanceOutcome
	state      reservationState
}

// reservationRegistry tracks live held reservations by approval id. Its mutex is
// the outer lock of the two-lock discipline (registry.mu -> e.mu, never
// reverse): a resolve takes registry.mu only to find the entry and flip its
// state, releases it, then takes e.mu to touch the native handle.
type reservationRegistry struct {
	by map[string]*heldReservation
	// resolved records the terminal outcome of ids resolved explicitly via
	// commit/rollback, so a repeated resolve is recognised as already-resolved
	// (conflict for commit, no-op for rollback) rather than unknown. TTL-swept
	// ids are NOT recorded here: an expired hold's token is already past its
	// expiry, so a later resolve legitimately sees it as unknown.
	resolved map[string]resolvedReservation
	mu       sync.Mutex
}

// newReservationRegistry builds an empty registry.
func newReservationRegistry() *reservationRegistry {
	return &reservationRegistry{
		by:       make(map[string]*heldReservation),
		resolved: make(map[string]resolvedReservation),
	}
}

// SetReservationStore attaches the persistence layer used to keep reservation
// intents durable. It must be called once before the first ReserveHold and
// before ReconcileOrphans; it is set at boot by the backend, which owns the
// store. Passing it after holds exist is unsupported.
func (e *openPitEngine) SetReservationStore(store ReservationStore) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resStore = store
}

// startSweeper launches the TTL sweeper goroutine. It is started once when the
// adapter is constructed and stopped by Stop via stopSweep.
func (e *openPitEngine) startSweeper() {
	e.sweepWG.Add(1)
	go func() {
		defer e.sweepWG.Done()
		ticker := time.NewTicker(sweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-e.stopSweep:
				return
			case <-ticker.C:
				e.sweepExpired(context.Background(), time.Now())
			}
		}
	}()
}

// sweepExpired rolls back every held reservation whose TTL has passed, via the
// rollback-by-id path. Collecting ids under registry.mu first keeps the lock
// order (registry.mu -> e.mu) intact during each rollback.
func (e *openPitEngine) sweepExpired(ctx context.Context, now time.Time) {
	e.registry.mu.Lock()
	e.pruneResolvedLocked(now)
	expired := make([]*heldReservation, 0)
	for _, held := range e.registry.by {
		if held.state == reservationStateHeld && !held.expiresAt.After(now) {
			expired = append(expired, held)
		}
	}
	e.registry.mu.Unlock()

	for _, held := range expired {
		// A concurrent confirm may have resolved the entry between collection and
		// here; rollbackHeld tolerates an already-resolved id. Swept ids are
		// forgotten (not recorded resolved): the token is already past expiry, so a
		// later resolve legitimately sees the id as unknown.
		_ = e.RunAccountSynchronized(ctx, held.account, func(lane AccountLane) error {
			return lane.(accountLane).rollbackHeldLane(ctx, held.approvalID, false)
		})
	}
}

// ReserveHold runs the pre-trade pipeline for o and, on accept, keeps the
// reservation held and registered under a fresh approval id.
func (e *openPitEngine) ReserveHold(
	ctx context.Context, o domain.Order,
) (HoldResult, error) {
	var result HoldResult
	err := e.RunAccountSynchronized(ctx, o.Account, func(lane fwengine.AccountLane) error {
		var err error
		result, err = lane.ReserveHold(ctx, o)
		return err
	})
	return result, err
}

func (l accountLane) ReserveHold(
	ctx context.Context, o domain.Order,
) (HoldResult, error) {
	if err := ctx.Err(); err != nil {
		return HoldResult{}, fmt.Errorf("engine: reserve hold cancelled: %w", err)
	}

	order, err := orderModelFromAccount(o, l.accountID)
	if err != nil {
		return HoldResult{}, err
	}

	l.owner.mu.RLock()
	store := l.owner.resStore
	l.owner.mu.RUnlock()

	reservation, rejects, err := l.eng.ExecutePreTrade(order)
	if err != nil {
		return HoldResult{}, fmt.Errorf("engine: execute pre-trade: %w", err)
	}
	if rejects != nil {
		return HoldResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	// Capture the serialized lock and the settlement estimate while the
	// reservation is held; do NOT close it.
	lockBytes, settlement, source, err := captureHold(reservation, o)
	if err != nil {
		reservation.RollbackAndClose()
		return HoldResult{}, err
	}
	outcomes := balanceOutcomesFromList(reservation.AccountAdjustments())

	now := time.Now().UTC()
	held := &heldReservation{
		res:        reservation,
		account:    o.Account,
		params:     o,
		lock:       lockBytes,
		settlement: settlement,
		source:     source,
		outcomes:   outcomes,
		approvalID: uuid.NewString(),
		issuedAt:   now,
		expiresAt:  now.Add(defaultHoldTTL),
		state:      reservationStateHeld,
	}

	l.owner.registry.mu.Lock()
	l.owner.registry.by[held.approvalID] = held
	l.owner.registry.mu.Unlock()

	if store != nil {
		if perr := persistIntent(ctx, store, held, domain.ReservationIntentStateHeld); perr != nil {
			// Persisting failed: roll the hold back so engine state and the (absent)
			// durable record agree, then surface the error.
			_ = l.rollbackHeldLane(ctx, held.approvalID, true)
			return HoldResult{}, perr
		}
	}

	return HoldResult{
		Accepted:            true,
		ApprovalID:          held.approvalID,
		Lock:                lockBytes,
		SettlementLockPrice: settlement,
		EstimateSource:      source,
		Outcomes:            outcomes,
		ExpiresAt:           held.expiresAt,
	}, nil
}

// CommitHeld commits the held reservation identified by approvalID.
func (e *openPitEngine) CommitHeld(ctx context.Context, approvalID string) error {
	account, err := e.reservationAccount(approvalID)
	if err != nil {
		if errors.Is(err, errAlreadyResolved) {
			return fmt.Errorf("engine: reservation %q already resolved: %w",
				approvalID, domain.ErrConflict)
		}
		return err
	}
	return e.RunAccountSynchronized(ctx, account, func(lane fwengine.AccountLane) error {
		return lane.CommitHeld(ctx, approvalID)
	})
}

func (l accountLane) CommitHeld(ctx context.Context, approvalID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: commit held cancelled: %w", err)
	}

	held, err := l.owner.beginResolve(approvalID)
	if err != nil {
		// A second commit on an already-resolved id is a conflict, not a panic.
		if errors.Is(err, errAlreadyResolved) {
			return fmt.Errorf("engine: reservation %q already resolved: %w",
				approvalID, domain.ErrConflict)
		}
		return err
	}

	held.res.CommitAndClose()

	// The in-memory single-resolve guard and native commit are the engine's only
	// job here. Durable persistence (intent flip + order status + event) is the
	// node's atomic ResolveOrderReservation; the engine no longer touches the
	// store on the node-driven commit path.
	l.owner.finishResolve(approvalID, reservationStateCommitted, true)
	return nil
}

// RollbackHeld rolls back the held reservation identified by approvalID. It is
// tolerant of an already-resolved id.
func (e *openPitEngine) RollbackHeld(ctx context.Context, approvalID string) error {
	account, err := e.reservationAccount(approvalID)
	if err != nil {
		if errors.Is(err, errAlreadyResolved) {
			return nil
		}
		return err
	}
	return e.RunAccountSynchronized(ctx, account, func(lane fwengine.AccountLane) error {
		return lane.RollbackHeld(ctx, approvalID)
	})
}

func (l accountLane) RollbackHeld(ctx context.Context, approvalID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: rollback held cancelled: %w", err)
	}
	return l.rollbackHeldLane(ctx, approvalID, true)
}

// rollbackHeld is the internal rollback-by-id path shared by RollbackHeld and
// the TTL sweeper. It tolerates an already-resolved (terminal) entry: such a
// call is a no-op. An unknown id returns domain.ErrNotFound. When record is true
// the terminal outcome is remembered so a later resolve is recognised as
// already-resolved; the sweeper passes false to forget swept ids.
func (l accountLane) rollbackHeldLane(ctx context.Context, approvalID string, record bool) error {
	held, err := l.owner.beginResolve(approvalID)
	if err != nil {
		// Tolerate a terminal/resolving entry: it was already resolved.
		if errors.Is(err, errAlreadyResolved) {
			return nil
		}
		return err
	}

	// Rollback is idempotent and tolerates a closed handle, so it is safe whether
	// or not the engine is still running; the native handle is only touched under
	// e.mu either way.
	held.res.RollbackAndClose()
	l.owner.mu.RLock()
	store := l.owner.resStore
	l.owner.mu.RUnlock()

	l.owner.finishResolve(approvalID, reservationStateRolledBack, record)

	// record==true is the node-driven cancel: the node performs the atomic
	// ResolveOrderReservation (intent flip + status + events), so the engine does
	// not touch the store. record==false is the TTL sweeper, which the engine owns
	// end to end: it resolves atomically through its own store handle here. A late
	// confirm that won the in-memory guard already removed the entry, so only one
	// of {confirm, sweep} reaches this point; if the order moved on meanwhile the
	// store's status guard returns ErrConflict, which the sweeper swallows.
	if record || store == nil {
		return nil
	}
	return resolveSweptRollback(ctx, store, held)
}

func (e *openPitEngine) reservationAccount(approvalID string) (domain.AccountID, error) {
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	held, ok := e.registry.by[approvalID]
	if !ok {
		if _, done := e.registry.resolved[approvalID]; done {
			return "", errAlreadyResolved
		}
		return "", fmt.Errorf("engine: reservation %q: %w", approvalID, domain.ErrNotFound)
	}
	return held.account, nil
}

// errAlreadyResolved is the internal sentinel beginResolve returns when the
// entry exists but is already terminal or being resolved. Callers translate it:
// rollback tolerates it (no-op), commit maps it to domain.ErrConflict.
var errAlreadyResolved = fmt.Errorf("reservation already resolved")

// beginResolve finds the entry, asserts it is Held, and flips it to Resolving
// under registry.mu. The flip happens before any native commit/rollback so a
// second concurrent or repeated resolve cannot reach the native handle twice -
// this is what prevents the binding's double-Commit panic. An unknown id
// returns domain.ErrNotFound; a non-Held entry returns errAlreadyResolved.
func (e *openPitEngine) beginResolve(approvalID string) (*heldReservation, error) {
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	held, ok := e.registry.by[approvalID]
	if !ok {
		// A previously recorded terminal outcome means the id was already resolved
		// (commit -> conflict, rollback -> no-op); otherwise it is unknown.
		if _, done := e.registry.resolved[approvalID]; done {
			return nil, errAlreadyResolved
		}
		return nil, fmt.Errorf("engine: reservation %q: %w", approvalID, domain.ErrNotFound)
	}
	if held.state != reservationStateHeld {
		return nil, errAlreadyResolved
	}
	held.state = reservationStateResolving
	return held, nil
}

// finishResolve sets the terminal state, removes the live entry, and (when record
// is true) remembers the terminal outcome so a later resolve is recognised as
// already-resolved.
func (e *openPitEngine) finishResolve(approvalID string, state reservationState, record bool) {
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	if held, ok := e.registry.by[approvalID]; ok {
		held.state = state
		delete(e.registry.by, approvalID)
	}
	if record {
		e.registry.resolved[approvalID] = resolvedReservation{
			state:     state,
			expiresAt: time.Now().UTC().Add(resolvedRetention),
		}
	}
}

func (e *openPitEngine) pruneResolvedLocked(now time.Time) {
	for id, resolved := range e.registry.resolved {
		if !resolved.expiresAt.After(now) {
			delete(e.registry.resolved, id)
		}
	}
}

// SubmitImmediate runs the pre-trade pipeline and, on accept, commits the
// reservation and settles a fill at the captured settlement lock price in the
// same call so the held amount nets to zero.
func (e *openPitEngine) SubmitImmediate(
	ctx context.Context, o domain.Order,
) (ImmediateResult, error) {
	var result ImmediateResult
	err := e.RunAccountSynchronized(ctx, o.Account, func(lane fwengine.AccountLane) error {
		var err error
		result, err = lane.SubmitImmediate(ctx, o)
		return err
	})
	return result, err
}

func (l accountLane) SubmitImmediate(
	ctx context.Context, o domain.Order,
) (ImmediateResult, error) {
	if err := ctx.Err(); err != nil {
		return ImmediateResult{}, fmt.Errorf("engine: submit immediate cancelled: %w", err)
	}

	order, err := orderModelFromAccount(o, l.accountID)
	if err != nil {
		return ImmediateResult{}, err
	}

	reservation, rejects, err := l.eng.ExecutePreTrade(order)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf("engine: execute pre-trade: %w", err)
	}
	if rejects != nil {
		return ImmediateResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	lockBytes, settlement, source, err := captureHold(reservation, o)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}

	fillQuantity, err := immediateFillQuantity(o, settlement)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	report, err := executionReportFromAccount(domain.ExecutionReportInput{
		BaseAsset:      o.BaseAsset,
		QuoteAsset:     o.QuoteAsset,
		FillQuantity:   fillQuantity,
		FillPrice:      settlement,
		LeavesQuantity: "0",
		LockPrice:      settlement,
		Account:        o.Account,
		Side:           o.Side,
		Order:          o.ExternalID,
		OrderStatus:    domain.OrderStatusFilled,
	}, l.accountID)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}

	// Ordering rationale: the SDK reservation exposes only Commit/Rollback, and a
	// fill is settled by the engine-level ApplyExecutionReport, not by the
	// reservation. The execution report settles against the reservation's reserved
	// state once it is realized, so the commit must happen first - a report applied
	// against an unrealized reservation would not net the held amount to zero.
	// Settle-then-commit is therefore not expressible with this SDK.
	reservation.CommitAndClose()

	// CommitAndClose has already closed the reservation, so there is no
	// compensating rollback available if the post-trade settlement fails. A
	// failure here leaves the funds committed but the fill unsettled, which only an
	// operator can reconcile; surface that explicitly rather than as a bare wrapped
	// error so the caller does not retry blindly.
	postTrade, err := l.eng.ApplyExecutionReport(report)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf(
			"engine: reservation committed but execution report failed for order %s "+
				"(account %s); funds are committed and the fill is unsettled - the "+
				"engine needs manual reconciliation: %w",
			orderExternalIDForError(o.ExternalID), o.Account, err,
		)
	}
	return ImmediateResult{
		Accepted:            true,
		Lock:                lockBytes,
		Blocks:              executionBlocksFrom(postTrade.AccountBlocks, o.Account),
		Outcomes:            balanceOutcomesFromList(postTrade.AccountAdjustmentOutcomes),
		SettlementLockPrice: settlement,
		FillQuantity:        fillQuantity,
		EstimateSource:      source,
	}, nil
}

func orderExternalIDForError(id domain.ExternalID) string {
	if id.IsZero() {
		return "<unassigned>"
	}
	return id.String()
}

// ReconcileOrphans reports persisted held reservation intents after a restart.
// Held effects are durable in balances and resolution can fall back to the
// intent row when the native handle is gone, so the reconcile step must not
// roll them back. It is a no-op (returns 0) when no store is attached.
func (e *openPitEngine) ReconcileOrphans(ctx context.Context) (int, error) {
	e.mu.Lock()
	store := e.resStore
	e.mu.Unlock()
	if store == nil {
		return 0, nil
	}

	intents, err := store.ListOpenReservationIntents(ctx)
	if err != nil {
		return 0, fmt.Errorf("engine: list open reservation intents: %w", err)
	}
	return len(intents), nil
}

// drainHeldLocked rolls back and closes every non-terminal registry entry. It is
// called by Stop under e.mu, before the engine stops, so no held reservation
// leaks a native handle across teardown. Callers must hold e.mu.
func (e *openPitEngine) drainHeldLocked() {
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	for id, held := range e.registry.by {
		if !held.state.terminal() {
			held.res.RollbackAndClose()
			held.state = reservationStateRolledBack
		}
		delete(e.registry.by, id)
	}
}

// captureHold serializes a held reservation's lock and derives its settlement
// estimate while the reservation is still held (not yet closed): the serialized
// lock bytes (persisted verbatim), the settlement-leg lock price as a decimal
// string, and the estimate source. The reservation lock is read exactly once,
// both serialized through the lock seam (for persistence) and read as prices
// (for the settlement estimate). The caller owns the resulting snapshot and may
// then commit or roll back the reservation.
func captureHold(reservation *pretrade.Reservation, o domain.Order) ([]byte, string, string, error) {
	lockBytes, err := serializeReservationLock(reservation)
	if err != nil {
		return nil, "", "", err
	}
	prices, err := reservation.Lock().Prices()
	if err != nil {
		return nil, "", "", fmt.Errorf("engine: read reservation lock: %w", err)
	}
	settlement, source := settlementEstimate(pricesToStrings(prices), o)
	return lockBytes, settlement, source, nil
}

// settlementEstimate derives the settlement-leg lock price and the estimate
// source for an order. The settlement leg is the last lock price (default-group
// records come first, the spot-funds settlement leg last); the source is "limit"
// when the order carried a limit price, else "market_mark".
func settlementEstimate(lockPrices []string, o domain.Order) (string, string) {
	settlement := ""
	if len(lockPrices) > 0 {
		settlement = lockPrices[len(lockPrices)-1]
	}
	source := domain.EstimateSourceMarketMark
	if o.Price != "" {
		source = domain.EstimateSourceLimit
	}
	return settlement, source
}

// persistIntent writes a reservation intent row for a held reservation in the
// given state. The order params plus persisted balance outcomes are marshalled
// into the opaque ParamsJSON; the SDK-serialized lock is carried verbatim as the
// intent's Lock blob (not as a decimal-array JSON). The order ref is the order's
// opaque external id, zero for an in-memory-only hold.
func persistIntent(
	ctx context.Context, store ReservationStore,
	held *heldReservation, state domain.ReservationIntentState,
) error {
	paramsJSON, err := json.Marshal(reservationIntentPayload{
		Order:    held.params,
		Outcomes: held.outcomes,
	})
	if err != nil {
		return fmt.Errorf("engine: marshal reservation params: %w", err)
	}
	intent := domain.ReservationIntent{
		ApprovalID: held.approvalID,
		Order:      held.params.ExternalID,
		Account:    held.account,
		ParamsJSON: string(paramsJSON),
		Lock:       held.lock,
		IssuedAt:   held.issuedAt,
		ExpiresAt:  held.expiresAt,
		State:      state,
	}
	if err := store.UpsertReservationIntent(ctx, intent); err != nil {
		return fmt.Errorf("engine: persist reservation intent: %w", err)
	}
	return nil
}

type reservationIntentPayload struct {
	Order    domain.Order     `json:"order"`
	Outcomes []BalanceOutcome `json:"outcomes,omitempty"`
}

// resolveSweptRollback atomically resolves a TTL-swept hold: it flips the intent
// to rolled-back, advances the order accepted->rolled_back, and appends one
// system-sourced reservation_rolled_back event in a single store transaction. An
// in-memory-only hold (no order row) carries OrderID==0, so the store skips the
// order/event writes and only flips the intent (tolerating its absence too). The
// status WHERE-guard (AllowedFrom={accepted}) is TOCTOU-safe: if a confirm/fill
// advanced the order between collection and this tx, the store returns
// ErrConflict and writes nothing; the caller (sweeper) swallows it, leaving the
// winning resolution intact.
func resolveSweptRollback(
	ctx context.Context, store ReservationStore, held *heldReservation,
) error {
	order := held.params.ExternalID
	var events []domain.OrderEvent
	if !order.IsZero() {
		events = []domain.OrderEvent{{
			Order:  order,
			Type:   domain.OrderEventReservationRolledBack,
			Source: domain.SourceSystem,
			// A TTL-swept rollback is system-initiated: it has no actor principal, so
			// the event carries an empty principal (a NULL reference). A literal
			// "system" code would point at a non-existent principal dictionary row.
		}}
	}
	err := store.ResolveOrderReservation(ctx, domain.ReservationResolution{
		ApprovalID:  held.approvalID,
		IntentState: domain.ReservationIntentStateRolledBack,
		OrderStatus: domain.OrderStatusRolledBack,
		AllowedFrom: []domain.OrderStatus{domain.OrderStatusAccepted},
		Events:      events,
		Order:       order,
	})
	if err != nil {
		// A swept order may have been committed/filled meanwhile: the status guard
		// returns ErrConflict and an unknown order returns ErrNotFound. Both are
		// expected races the sweeper tolerates - the winning resolution stands.
		if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("engine: resolve swept reservation: %w", err)
	}
	return nil
}
