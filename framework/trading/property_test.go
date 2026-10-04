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

package trading_test

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/trading"
)

// The core, rather than a model sink, is the oracle for persisted trades.
// Random streams omit and reorder fills; Lookup always returns venue truth.
func TestRandomizedFillConvergence(t *testing.T) {
	f := newFixture(t)
	f.start()
	for seed := int64(0); seed < 30; seed++ {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			count := 2 + rng.Intn(6)
			cancelled := rng.Intn(2) == 0
			cumulative := decimal.Zero
			fills := make([]trading.Fill, count)
			for i := range fills {
				qty := decimal.NewFromInt(int64(1 + rng.Intn(200))).Shift(-3)
				cumulative = cumulative.Add(qty)
				fills[i] = makeFill(qty.String(), cumulative.String(), "0", false)
				fills[i].Price = decimal.NewFromInt(int64(100 + rng.Intn(100))).Shift(-2).String()
			}
			requested := cumulative
			if cancelled {
				requested = requested.Add(decimal.NewFromInt(1))
			}
			for i := range fills {
				cum, err := decimal.NewFromString(fills[i].CumQuantity)
				must(t, err)
				fills[i].LeavesQuantity = requested.Sub(cum).String()
			}
			status := trading.VenueStatusCancelled
			officerStatus := domain.OrderStatusCancelled
			if !cancelled {
				status = trading.VenueStatusFilled
				officerStatus = domain.OrderStatusFilled
				fills[count-1].Final = true
			}
			order := f.submit(requested.String())
			link := f.send(order)
			var delivery []int
			for i := 0; i < count-1; i++ {
				f.venue.setSnapshot(link, trading.VenueStatusOpen,
					fills[i].CumQuantity, fills[:i+1]...)
				if rng.Intn(3) != 0 {
					delivery = append(delivery, i)
				}
				if rng.Intn(2) == 0 {
					delivery = append(delivery, i, i)
				}
				rng.Shuffle(len(delivery), func(a, b int) {
					delivery[a], delivery[b] = delivery[b], delivery[a]
				})
				for len(delivery) > 0 && rng.Intn(2) == 0 {
					last := len(delivery) - 1
					f.fill(link, fills[delivery[last]])
					delivery = delivery[:last]
				}
				f.flush()
				if domain.OrderStatusTerminal(f.detail(order).Order.Status) {
					t.Fatal("Officer became terminal before the venue terminal event")
				}
			}
			f.venue.setSnapshot(link, status, cumulative.String(), fills...)
			f.orderEvent(link)
			delivery = append(delivery, count-1, count-1)
			rng.Shuffle(len(delivery), func(a, b int) {
				delivery[a], delivery[b] = delivery[b], delivery[a]
			})
			for _, i := range delivery {
				f.fill(link, fills[i])
			}
			f.flush()
			detail := f.detail(order)
			if detail.Order.Status != officerStatus {
				t.Fatalf("venue=%s Officer=%s", status, detail.Order.Status)
			}
			assertTrades(t, detail, fills)
			for _, fill := range fills {
				cum, err := decimal.NewFromString(fill.CumQuantity)
				must(t, err)
				id := domain.ExternalID("trading:" + link.ClientOrderID + ":fill:" + cum.String())
				exists, err := f.st.ExecutionReportExists(testCtx, id)
				must(t, err)
				if !exists {
					t.Fatalf("missing deterministic fill report %s", id)
				}
				reports := 0
				for _, event := range detail.Events {
					if event.Payload.ExecutionReport != nil &&
						event.Payload.ExecutionReport.ExternalID == id {
						reports++
					}
				}
				if reports != 1 {
					t.Fatalf("fill report %s history entries = %d, want 1", id, reports)
				}
			}
			f.assertReleased(order)
		})
	}
}
