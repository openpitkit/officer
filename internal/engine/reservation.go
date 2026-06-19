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

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"go.openpit.dev/openpit/pretrade"

	"go.openpit.dev/officer/internal/domain"
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
	lockPrices []string
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

// ReservationStore is the slice of the persistence layer the engine adapter
// uses to keep reservation intents durable across the hold lifecycle. It is the
// store's reservation-intent methods; the adapter records intent metadata while
// the node persists balance effects. It is optional: when nil the adapter keeps
// holds in memory only.
type ReservationStore interface {
	UpsertReservationIntent(ctx context.Context, intent domain.ReservationIntent) error
	ListOpenReservationIntents(ctx context.Context) ([]domain.ReservationIntent, error)
	SetReservationIntentState(
		ctx context.Context, approvalID string, state domain.ReservationIntentState,
	) error
	AppendOrderEvent(ctx context.Context, ev domain.OrderEvent) (domain.OrderEvent, error)
	UpdateOrderStatus(
		ctx context.Context, tenant domain.TenantID, id int64, status domain.OrderStatus,
	) error
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
	expired := make([]string, 0)
	for id, held := range e.registry.by {
		if held.state == reservationStateHeld && !held.expiresAt.After(now) {
			expired = append(expired, id)
		}
	}
	e.registry.mu.Unlock()

	for _, id := range expired {
		// A concurrent confirm may have resolved the entry between collection and
		// here; rollbackHeld tolerates an already-resolved id. Swept ids are
		// forgotten (not recorded resolved): the token is already past expiry, so a
		// later resolve legitimately sees the id as unknown.
		_ = e.rollbackHeld(ctx, id, false)
	}
}

// ReserveHold runs the pre-trade pipeline for o and, on accept, keeps the
// reservation held and registered under a fresh approval id.
func (e *openPitEngine) ReserveHold(
	ctx context.Context, o domain.Order,
) (HoldResult, error) {
	if err := ctx.Err(); err != nil {
		return HoldResult{}, fmt.Errorf("engine: reserve hold cancelled: %w", err)
	}

	order, err := orderModelFrom(o)
	if err != nil {
		return HoldResult{}, err
	}

	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return HoldResult{}, fmt.Errorf("engine: reserve hold on stopped engine")
	}
	store := e.resStore

	reservation, rejects, err := e.eng.ExecutePreTrade(order)
	if err != nil {
		e.mu.Unlock()
		return HoldResult{}, fmt.Errorf("engine: execute pre-trade: %w", err)
	}
	if rejects != nil {
		e.mu.Unlock()
		return HoldResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	// Capture the lock prices while the reservation is held; do NOT close it.
	lockPrices, err := lockPricesFrom(reservation)
	if err != nil {
		reservation.RollbackAndClose()
		e.mu.Unlock()
		return HoldResult{}, err
	}
	outcomes := balanceOutcomesFromList(reservation.AccountAdjustments())
	e.mu.Unlock()

	now := time.Now().UTC()
	held := &heldReservation{
		res:        reservation,
		account:    o.Account,
		params:     o,
		lockPrices: lockPrices,
		outcomes:   outcomes,
		approvalID: uuid.NewString(),
		issuedAt:   now,
		expiresAt:  now.Add(defaultHoldTTL),
		state:      reservationStateHeld,
	}

	e.registry.mu.Lock()
	e.registry.by[held.approvalID] = held
	e.registry.mu.Unlock()

	if store != nil {
		if perr := persistIntent(ctx, store, held, domain.ReservationIntentStateHeld); perr != nil {
			// Persisting failed: roll the hold back so engine state and the (absent)
			// durable record agree, then surface the error.
			_ = e.rollbackHeld(ctx, held.approvalID, true)
			return HoldResult{}, perr
		}
	}

	settlement, source := settlementEstimate(lockPrices, o)
	return HoldResult{
		Accepted:            true,
		ApprovalID:          held.approvalID,
		LockPrices:          lockPrices,
		SettlementLockPrice: settlement,
		EstimateSource:      source,
		Outcomes:            outcomes,
		ExpiresAt:           held.expiresAt,
	}, nil
}

// CommitHeld commits the held reservation identified by approvalID.
func (e *openPitEngine) CommitHeld(ctx context.Context, approvalID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: commit held cancelled: %w", err)
	}

	held, err := e.beginResolve(approvalID)
	if err != nil {
		// A second commit on an already-resolved id is a conflict, not a panic.
		if errors.Is(err, errAlreadyResolved) {
			return fmt.Errorf("engine: reservation %q already resolved: %w",
				approvalID, domain.ErrConflict)
		}
		return err
	}

	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		e.abortResolve(approvalID)
		return fmt.Errorf("engine: commit held on stopped engine")
	}
	held.res.CommitAndClose()
	store := e.resStore
	e.mu.Unlock()

	e.finishResolve(approvalID, reservationStateCommitted, true)
	if store != nil {
		return setIntentState(ctx, store, approvalID, domain.ReservationIntentStateCommitted)
	}
	return nil
}

// RollbackHeld rolls back the held reservation identified by approvalID. It is
// tolerant of an already-resolved id.
func (e *openPitEngine) RollbackHeld(ctx context.Context, approvalID string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("engine: rollback held cancelled: %w", err)
	}
	return e.rollbackHeld(ctx, approvalID, true)
}

// rollbackHeld is the internal rollback-by-id path shared by RollbackHeld and
// the TTL sweeper. It tolerates an already-resolved (terminal) entry: such a
// call is a no-op. An unknown id returns domain.ErrNotFound. When record is true
// the terminal outcome is remembered so a later resolve is recognised as
// already-resolved; the sweeper passes false to forget swept ids.
func (e *openPitEngine) rollbackHeld(ctx context.Context, approvalID string, record bool) error {
	held, err := e.beginResolve(approvalID)
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
	e.mu.Lock()
	held.res.RollbackAndClose()
	store := e.resStore
	e.mu.Unlock()

	e.finishResolve(approvalID, reservationStateRolledBack, record)
	if store != nil {
		if err := setIntentState(ctx, store, approvalID, domain.ReservationIntentStateRolledBack); err != nil {
			return err
		}
		if !record {
			return markSweptOrderRolledBack(ctx, store, held)
		}
	}
	return nil
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
	e.pruneResolvedLocked(time.Now().UTC())
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

func (e *openPitEngine) abortResolve(approvalID string) {
	e.registry.mu.Lock()
	defer e.registry.mu.Unlock()
	if held, ok := e.registry.by[approvalID]; ok && held.state == reservationStateResolving {
		held.state = reservationStateHeld
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
	if err := ctx.Err(); err != nil {
		return ImmediateResult{}, fmt.Errorf("engine: submit immediate cancelled: %w", err)
	}

	order, err := orderModelFrom(o)
	if err != nil {
		return ImmediateResult{}, err
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return ImmediateResult{}, fmt.Errorf("engine: submit immediate on stopped engine")
	}

	reservation, rejects, err := e.eng.ExecutePreTrade(order)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf("engine: execute pre-trade: %w", err)
	}
	if rejects != nil {
		return ImmediateResult{Accepted: false, Rejects: orderRejectsFrom(rejects)}, nil
	}

	lockPrices, err := lockPricesFrom(reservation)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	settlement, source := settlementEstimate(lockPrices, o)

	fillQuantity, err := immediateFillQuantity(o, settlement)
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}
	report, err := executionReportFrom(domain.ExecutionReportInput{
		BaseAsset:    o.BaseAsset,
		QuoteAsset:   o.QuoteAsset,
		FillQuantity: fillQuantity,
		FillPrice:    settlement,
		LockPrice:    settlement,
		Account:      o.Account,
		Side:         o.Side,
		OrderID:      o.ID,
		Final:        true,
	})
	if err != nil {
		reservation.RollbackAndClose()
		return ImmediateResult{}, err
	}

	// Commit realizes the reservation; ApplyExecutionReport at the same lock price
	// nets the held amount to zero. Commit alone does not realize a fill.
	reservation.CommitAndClose()

	postTrade, err := e.eng.ApplyExecutionReport(report)
	if err != nil {
		return ImmediateResult{}, fmt.Errorf("engine: apply execution report: %w", err)
	}
	return ImmediateResult{
		Accepted:            true,
		LockPrices:          lockPrices,
		Blocks:              executionBlocksFrom(postTrade.AccountBlocks, o.Account),
		Outcomes:            balanceOutcomesFromList(postTrade.AccountAdjustmentOutcomes),
		SettlementLockPrice: settlement,
		FillQuantity:        fillQuantity,
		EstimateSource:      source,
	}, nil
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

// lockPricesFrom reads a reservation's lock prices as decimal strings. The
// reservation must be held (not yet closed); the caller owns the resulting
// snapshot.
func lockPricesFrom(reservation *pretrade.Reservation) ([]string, error) {
	prices, err := reservation.Lock().Prices()
	if err != nil {
		return nil, fmt.Errorf("engine: read reservation lock: %w", err)
	}
	out := make([]string, 0, len(prices))
	for _, price := range prices {
		out = append(out, price.String())
	}
	return out, nil
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
// given state. Params and lock prices are marshalled to JSON.
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
	pricesJSON, err := json.Marshal(held.lockPrices)
	if err != nil {
		return fmt.Errorf("engine: marshal reservation lock prices: %w", err)
	}
	intent := domain.ReservationIntent{
		ApprovalID:     held.approvalID,
		OrderID:        held.params.ID,
		Account:        held.account,
		ParamsJSON:     string(paramsJSON),
		LockPricesJSON: string(pricesJSON),
		IssuedAt:       held.issuedAt,
		ExpiresAt:      held.expiresAt,
		State:          state,
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

// setIntentState marks a persisted intent terminal, tolerating an absent row
// (an in-memory-only hold left no row to update).
func setIntentState(
	ctx context.Context, store ReservationStore,
	approvalID string, state domain.ReservationIntentState,
) error {
	if err := store.SetReservationIntentState(ctx, approvalID, state); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("engine: persist reservation state: %w", err)
	}
	return nil
}

func markSweptOrderRolledBack(
	ctx context.Context, store ReservationStore, held *heldReservation,
) error {
	if held.params.ID == 0 {
		return nil
	}
	tenant := held.params.Tenant
	if tenant == "" {
		tenant = domain.DefaultTenant
	}
	if _, err := store.AppendOrderEvent(ctx, domain.OrderEvent{
		OrderID:   held.params.ID,
		Type:      domain.OrderEventReservationRolledBack,
		Source:    domain.SourceSystem,
		Principal: "system",
	}); err != nil {
		return fmt.Errorf("engine: append swept reservation rollback event: %w", err)
	}
	if err := store.UpdateOrderStatus(
		ctx, tenant, held.params.ID, domain.OrderStatusRolledBack,
	); err != nil {
		return fmt.Errorf("engine: mark swept order rolled back: %w", err)
	}
	return nil
}
