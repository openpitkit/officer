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

	"go.openpit.dev/officer/framework/domain"
)

// ReservationStore is the slice of the persistence layer the engine adapter
// uses to keep reservation intents durable across the hold lifecycle. It is the
// store's reservation-intent methods; the adapter records intent metadata while
// the node persists balance effects. It is optional: when nil the adapter keeps
// holds in memory only.
type ReservationStore interface {
	UpsertReservationIntent(ctx context.Context, intent domain.ReservationIntent) error
	ListOpenReservationIntents(ctx context.Context) ([]domain.ReservationIntent, error)
	// ResolveOrderReservation flips the intent state, advances the order status
	// under an AllowedFrom WHERE-guard, and appends the lifecycle event(s) in one
	// atomic store transaction. The TTL sweeper calls it directly so a swept
	// rollback is all-or-nothing; the node drives confirm/cancel through the same
	// method. F1 (no node->engine coupling) is preserved: the engine resolves via
	// its own store handle, never a node reference.
	ResolveOrderReservation(ctx context.Context, r domain.ReservationResolution) error
}
