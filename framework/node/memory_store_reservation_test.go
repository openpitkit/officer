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

package node

import (
	"context"
	"slices"
	"time"

	"go.openpit.dev/officer/framework/domain"
)

func (r *memoryRealm) UpsertReservationIntent(
	_ context.Context, intent domain.ReservationIntent,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reservations[intent.ApprovalID] = intent
	return nil
}

func (r *memoryRealm) GetReservationIntent(
	_ context.Context, approvalID string,
) (domain.ReservationIntent, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	intent, ok := r.reservations[approvalID]
	return intent, ok, nil
}

func (r *memoryRealm) GetOpenReservationIntentByOrder(
	_ context.Context, order domain.ExternalID,
) (domain.ReservationIntent, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, intent := range r.reservations {
		if intent.Order == order && intent.State == domain.ReservationIntentStateHeld {
			return intent, true, nil
		}
	}
	return domain.ReservationIntent{}, false, nil
}

func (r *memoryRealm) ListOpenReservationIntents(
	context.Context,
) ([]domain.ReservationIntent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []domain.ReservationIntent
	for _, intent := range r.reservations {
		if intent.State == domain.ReservationIntentStateHeld {
			out = append(out, intent)
		}
	}
	return out, nil
}

func (r *memoryRealm) SetReservationIntentState(
	_ context.Context, approvalID string, state domain.ReservationIntentState,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	intent, ok := r.reservations[approvalID]
	if !ok {
		return domain.ErrNotFound
	}
	intent.State = state
	r.reservations[approvalID] = intent
	return nil
}

func (r *memoryRealm) ResolveOrderReservation(
	_ context.Context, resolution domain.ReservationResolution,
) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	intent, ok := r.reservations[resolution.ApprovalID]
	if ok {
		intent.State = resolution.IntentState
		r.reservations[resolution.ApprovalID] = intent
	}
	if resolution.Order.IsZero() {
		return nil
	}
	order, ok := r.orders[resolution.Order]
	if !ok {
		return domain.ErrNotFound
	}
	if len(resolution.AllowedFrom) > 0 &&
		!slices.Contains(resolution.AllowedFrom, order.Status) {
		return domain.ErrConflict
	}
	order.Status = resolution.OrderStatus
	r.orders[resolution.Order] = order
	for _, event := range resolution.Events {
		if event.ExternalID.IsZero() {
			event.ExternalID = r.nextExternalID()
		}
		if event.At.IsZero() {
			event.At = time.Now().UTC()
		}
		r.events = append(r.events, event)
	}
	return nil
}
