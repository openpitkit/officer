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
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/scmhub/ibapi"
	"github.com/shopspring/decimal"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

// Like framework/trading.TestRandomizedFillConvergence, this uses fixed seeds
// and cumulative quantity as the runtime's identity and continuity barrier.
func TestIBRandomizedFillExactlyOnce(t *testing.T) {
	for _, seed := range []int64{1, 7, 29, 61, 109, 997} {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			v := newFakeIBVenue()
			c := testIBConnector(t, v)
			events := subscribeIB(t, c)
			rng := rand.New(rand.NewSource(seed))
			officerCum, officerFee := decimal.Zero, decimal.Zero
			for session := 0; session < 3; session++ {
				v.mu.Lock()
				client := v.clients[len(v.clients)-1]
				v.mu.Unlock()
				var callbacks []func()
				cum := 0
				firstExecution, wantFills := 1, 4
				if session == 2 {
					firstExecution, wantFills = 3, 2
					officerCum, officerFee = decimal.NewFromInt(3), decimal.RequireFromString("-0.1")
				}
				for i := 1; i <= 4; i++ {
					cum += i
					if i < firstExecution {
						continue
					}
					execution := ibTestExecution(fmt.Sprint(i), fmt.Sprint(i), fmt.Sprint(cum))
					// The pump may only wait behind predecessors already held by this session.
					client.w.ExecDetails(-1, ibapi.NewContract(), execution)
					fee := float64(i) / 10
					if i == 2 {
						fee = -fee
					}
					report := ibapi.CommissionAndFeesReport{ExecID: execution.ExecID, CommissionAndFees: fee, Currency: "USD"}
					for n := 0; n < 1+rng.Intn(4); n++ {
						callbacks = append(callbacks,
							func() { client.w.ExecDetails(-1, ibapi.NewContract(), execution) },
							func() { client.w.CommissionAndFeesReport(report) },
							func() {
								client.w.OpenOrder(42, ibapi.NewContract(), ibTestVenueOrder(), &ibapi.OrderState{Status: "Submitted"})
							},
							func() {
								client.w.OrderStatus(42, "Submitted", execution.CumQty, ibapi.StringToDecimal(fmt.Sprint(10-int(execution.CumQty.Int()))), 100, 4242, 0, 0, 0, "", 0)
							},
						)
					}
				}
				rng.Shuffle(len(callbacks), func(i, j int) { callbacks[i], callbacks[j] = callbacks[j], callbacks[i] })
				for _, callback := range callbacks {
					callback()
				}
				seen := make(map[string]bool)
				last := decimal.Zero
				if session == 2 {
					last = decimal.NewFromInt(3)
				}
				timer := time.NewTimer(time.Second)
				quiet := time.NewTimer(30 * time.Millisecond)
				defer timer.Stop()
				defer quiet.Stop()
			collect:
				for {
					select {
					case event := <-events:
						if event.Ref.ClientOrderID != "officer-order" {
							t.Fatalf("missing Officer ref: %+v", event)
						}
						if event.Kind != fwtrading.EventFill {
							continue
						}
						fill := event.Fill
						if seen[fill.CumQuantity] {
							t.Fatalf("fill emitted twice in session %d at cumulative %s", session, fill.CumQuantity)
						}
						seen[fill.CumQuantity] = true
						cum, _ := decimal.NewFromString(fill.CumQuantity)
						qty, _ := decimal.NewFromString(fill.Quantity)
						if cum.LessThanOrEqual(last) || !cum.Sub(qty).Equal(last) {
							t.Fatalf("non-monotone or missing fill: last=%s fill=%+v", last, fill)
						}
						last = cum
						if fill.Commission == nil || fill.Commission.Currency != "USD" || fill.At.IsZero() {
							t.Fatalf("incomplete fill: %+v", fill)
						}
						expectedFee := qty.Shift(-1)
						if qty.Equal(decimal.NewFromInt(2)) {
							expectedFee = expectedFee.Neg()
						}
						if fill.Commission.Amount != expectedFee.String() || fill.LeavesQuantity != decimal.NewFromInt(10).Sub(cum).String() || fill.Final != cum.Equal(decimal.NewFromInt(10)) {
							t.Fatalf("wrong execution or commission join: %+v", fill)
						}
						// This is handleFill's cumulative dedup and continuity rule.
						if cum.GreaterThan(officerCum) {
							if !cum.Sub(qty).Equal(officerCum) {
								t.Fatal("runtime continuity gap")
							}
							fee, _ := decimal.NewFromString(fill.Commission.Amount)
							officerCum, officerFee = cum, officerFee.Add(fee)
						}
					case <-quiet.C:
						if len(seen) == wantFills {
							break collect
						}
						quiet.Reset(30 * time.Millisecond)
					case <-timer.C:
						t.Fatalf("session %d emitted %d fills, want %d (predecessors may predate session)", session, len(seen), wantFills)
					}
				}
				// Replay after every fill has been delivered. This exercises
				// session-local execId suppression independently of batching.
				for _, callback := range callbacks {
					callback()
				}
				replayTimer := time.NewTimer(30 * time.Millisecond)
			forReplay:
				for {
					select {
					case event := <-events:
						if event.Kind == fwtrading.EventFill {
							replayTimer.Stop()
							t.Fatalf("fill emitted twice on replay in session %d", session)
						}
					case <-replayTimer.C:
						break forReplay
					}
				}
				if session < 2 {
					client.w.ConnectionClosed()
					if event := nextIBEvent(t, events); event.Kind != fwtrading.EventConnected {
						t.Fatalf("reconnect event=%+v", event)
					}
				}
			}
			if officerCum.String() != "10" || officerFee.String() != "0.6" {
				t.Fatalf("double-counted fills/fees: qty=%s fee=%s", officerCum, officerFee)
			}
			c.Close()
			if _, ok := <-events; ok {
				t.Fatal("Close did not close the stream")
			}
			if v.connects != 3 || v.disconnects != 3 {
				t.Fatalf("reconnect sessions leaked: %d/%d", v.connects, v.disconnects)
			}
		})
	}
}

func TestIBUnsetCommissionWaits(t *testing.T) {
	v := newFakeIBVenue()
	c := testIBConnector(t, v)
	events := subscribeIB(t, c)
	v.mu.Lock()
	client := v.clients[0]
	v.mu.Unlock()
	client.w.OpenOrder(42, ibapi.NewContract(), ibTestVenueOrder(), &ibapi.OrderState{Status: "Submitted"})
	client.w.ExecDetails(-1, ibapi.NewContract(), ibTestExecution("one", "10", "10"))
	client.w.CommissionAndFeesReport(ibapi.CommissionAndFeesReport{ExecID: "one", CommissionAndFees: ibapi.UNSET_FLOAT, Currency: "USD"})
	timer := time.NewTimer(30 * time.Millisecond)
	defer timer.Stop()
wait:
	for {
		select {
		case event := <-events:
			if event.Kind == fwtrading.EventFill {
				t.Fatal("unset commission released a fill")
			}
		case <-timer.C:
			break wait
		}
	}
	client.w.CommissionAndFeesReport(ibapi.CommissionAndFeesReport{ExecID: "one", CommissionAndFees: -0.25, Currency: "USD"})
	for {
		event := nextIBEvent(t, events)
		if event.Kind != fwtrading.EventFill {
			continue
		}
		if event.Fill.Commission.Amount != "-0.25" || !event.Fill.Final || event.Fill.LeavesQuantity != "0" {
			t.Fatalf("fill=%+v", event.Fill)
		}
		break
	}
}

func TestIBHeldExecutionsBlockOnlyTheirOrder(t *testing.T) {
	for _, missing := range []string{"commission", "total"} {
		t.Run(missing, func(t *testing.T) {
			v := newFakeIBVenue()
			c := testIBConnector(t, v)
			s := ibTestSession(t, c, v)
			if missing != "total" {
				s.OpenOrder(42, ibapi.NewContract(), ibTestVenueOrder(), &ibapi.OrderState{Status: "Submitted"})
			}
			s.ExecDetails(-1, ibapi.NewContract(), ibTestExecution("low", "4", "4"))
			s.ExecDetails(-1, ibapi.NewContract(), ibTestExecution("high", "6", "10"))
			s.CommissionAndFeesReport(ibapi.CommissionAndFeesReport{ExecID: "high", CommissionAndFees: 0.5, Currency: "USD"})
			if missing != "commission" {
				s.CommissionAndFeesReport(ibapi.CommissionAndFeesReport{ExecID: "low", CommissionAndFees: 0.5, Currency: "USD"})
			}
			other := ibTestVenueOrder()
			other.OrderRef, other.OrderID = "other-order", 43
			other.TotalQuantity = ibapi.StringToDecimal("1")
			s.OpenOrder(43, ibapi.NewContract(), other, &ibapi.OrderState{Status: "Submitted"})
			e := ibTestExecution("other", "1", "1")
			e.OrderID, e.OrderRef = 43, "other-order"
			s.ExecDetails(-1, ibapi.NewContract(), e)
			s.CommissionAndFeesReport(ibapi.CommissionAndFeesReport{ExecID: "other", CommissionAndFees: 0.5, Currency: "USD"})
			events, _ := s.events()
			fills := 0
			for _, event := range events {
				if event.Kind == fwtrading.EventFill {
					fills++
					if event.Ref.ClientOrderID != "other-order" {
						t.Fatalf("released fill past held predecessor: %+v", event)
					}
				}
			}
			if fills != 1 {
				t.Fatalf("held order blocked another order: %+v", events)
			}
			if missing == "commission" {
				s.CommissionAndFeesReport(ibapi.CommissionAndFeesReport{ExecID: "low", CommissionAndFees: 0.5, Currency: "USD"})
			} else {
				s.OpenOrder(42, ibapi.NewContract(), ibTestVenueOrder(), &ibapi.OrderState{Status: "Submitted"})
			}
			events, _ = s.events()
			var cumulatives []string
			for _, event := range events {
				if event.Kind == fwtrading.EventFill {
					cumulatives = append(cumulatives, event.Fill.CumQuantity)
				}
			}
			if len(cumulatives) != 2 || cumulatives[0] != "4" || cumulatives[1] != "10" {
				t.Fatalf("released fills=%v", cumulatives)
			}
		})
	}
}
