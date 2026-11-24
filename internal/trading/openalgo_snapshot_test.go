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
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
	"go.openpit.dev/officer/internal/openalgo"
)

func TestOpenAlgoSnapshot(t *testing.T) {
	order := openalgo.OrderStatus{
		OrderID: "order-private", Symbol: "symbol-private", Exchange: "exchange-private",
		Action: "BUY", Quantity: "10.00", Status: "complete",
		Price: "invalid-unused-price", AveragePrice: "invalid-unused-average",
	}
	trade := openalgo.Trade{
		OrderID: order.OrderID, Symbol: order.Symbol, Exchange: order.Exchange,
		Action: order.Action, Quantity: "10.0", AveragePrice: "2.00",
	}
	filled := fwtrading.Snapshot{
		VenueOrderID: string(order.OrderID), Status: fwtrading.VenueStatusFilled,
		FilledQuantity: "10", Fills: []fwtrading.Fill{{
			Quantity: "10", Price: "2", CumQuantity: "10", LeavesQuantity: "0", Final: true,
		}},
	}
	type testCase struct {
		name     string
		status   string
		trades   []openalgo.Trade
		terminal openalgo.Decimal
		want     fwtrading.Snapshot
	}
	tests := []testCase{
		{name: "complete-exact", status: "complete", trades: []openalgo.Trade{trade}, want: filled},
		{
			name: "complete-ignores-terminal", status: "complete",
			trades: []openalgo.Trade{trade}, terminal: "invalid-unused", want: filled,
		},
	}
	var unordered []openalgo.Trade
	for _, values := range [][2]openalgo.Decimal{
		{"2.00", "10"}, {"1.0", "2.00"}, {"1.00", "10.0"}, {"6", "3.0"},
	} {
		row := trade
		row.Quantity, row.AveragePrice = values[0], values[1]
		unordered = append(unordered, row)
	}
	tests = append(tests, testCase{
		name: "complete-canonical-order", status: "complete", trades: unordered,
		want: fwtrading.Snapshot{
			VenueOrderID: string(order.OrderID), Status: fwtrading.VenueStatusFilled,
			FilledQuantity: "10", Fills: []fwtrading.Fill{
				{Quantity: "1", Price: "2", CumQuantity: "1", LeavesQuantity: "9"},
				{Quantity: "6", Price: "3", CumQuantity: "7", LeavesQuantity: "3"},
				{Quantity: "1", Price: "10", CumQuantity: "8", LeavesQuantity: "2"},
				{Quantity: "2", Price: "10", CumQuantity: "10", LeavesQuantity: "0", Final: true},
			},
		},
	})
	for _, status := range []string{"open", "pending", "trigger pending"} {
		tests = append(tests, testCase{
			name: status + "-ignores-rows", status: status,
			trades: []openalgo.Trade{{OrderID: order.OrderID}}, terminal: "invalid-unused",
			want: fwtrading.Snapshot{
				VenueOrderID: string(order.OrderID), Status: fwtrading.VenueStatusOpen,
				FilledQuantity: "0",
			},
		})
	}
	for _, status := range []string{"cancelled", "rejected", "expired"} {
		for _, terminal := range []openalgo.Decimal{"0", "0.00"} {
			tests = append(tests, testCase{
				name: status + "-empty-" + string(terminal), status: status, terminal: terminal,
				want: fwtrading.Snapshot{
					VenueOrderID: string(order.OrderID), Status: fwtrading.VenueStatusCancelled,
					FilledQuantity: "0", Reason: status,
				},
			})
		}
		for _, qty := range []openalgo.Decimal{"2.50", "10.00"} {
			row := trade
			row.Quantity = qty
			canonical := decimal.RequireFromString(string(qty)).String()
			tests = append(tests, testCase{
				name: status + "-confirmed-" + canonical, status: status,
				trades: []openalgo.Trade{row}, terminal: qty,
				want: fwtrading.Snapshot{
					VenueOrderID: string(order.OrderID), Status: fwtrading.VenueStatusCancelled,
					FilledQuantity: canonical, Reason: status, Fills: []fwtrading.Fill{{
						Quantity: canonical, Price: "2", CumQuantity: canonical,
						LeavesQuantity: decimal.NewFromInt(10).Sub(decimal.RequireFromString(string(qty))).String(),
					}},
				},
			})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := order
			status.Status = tt.status
			other := openalgo.Trade{OrderID: "other-private"}
			rows := append([]openalgo.Trade{other}, tt.trades...)
			before := append([]openalgo.Trade(nil), rows...)
			got, err := openAlgoSnapshot(status, rows, tt.terminal)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("snapshot=%+v err=%v, want %+v", got, err, tt.want)
			}
			if !reflect.DeepEqual(rows, before) {
				t.Fatal("snapshot mutated input rows")
			}
		})
	}
}

func TestOpenAlgoSnapshotErrors(t *testing.T) {
	order := openalgo.OrderStatus{
		OrderID: "order-private", Symbol: "symbol-private", Exchange: "exchange-private",
		Action: "BUY", Quantity: "10", Status: "complete",
	}
	trade := openalgo.Trade{
		OrderID: order.OrderID, Symbol: order.Symbol, Exchange: order.Exchange,
		Action: order.Action, Quantity: "10", AveragePrice: "2",
	}
	type testCase struct {
		name     string
		order    openalgo.OrderStatus
		trades   []openalgo.Trade
		terminal openalgo.Decimal
		sentinel error
		message  string
	}
	var tests []testCase
	for _, qty := range []openalgo.Decimal{"", "bad-private", "0", "0.00", "-1"} {
		status := order
		status.Quantity = qty
		tests = append(tests, testCase{
			name: "order-quantity-" + string(qty), order: status, message: "order quantity",
		})
	}
	missingID := order
	missingID.OrderID = ""
	tests = append(tests, testCase{name: "missing-id", order: missingID, message: "order id"})
	for _, unknown := range []string{"", "Complete", "cancelled ", "unknown-private"} {
		status := order
		status.Status = unknown
		tests = append(tests, testCase{
			name: "unknown-status-" + unknown, order: status, message: "unknown order status",
		})
	}
	for _, field := range []string{"symbol", "exchange", "action"} {
		row := trade
		switch field {
		case "symbol":
			row.Symbol = "Symbol-private"
		case "exchange":
			row.Exchange = "Exchange-private"
		case "action":
			row.Action = "buy"
		}
		tests = append(tests, testCase{
			name: "mismatched-" + field, order: order,
			trades: []openalgo.Trade{row}, message: "trade identity",
		})
	}
	for _, value := range []openalgo.Decimal{"", "bad-private", "0", "0.0", "-1"} {
		for _, field := range []string{"quantity", "average price"} {
			row := trade
			if field == "quantity" {
				row.Quantity = value
			} else {
				row.AveragePrice = value
			}
			tests = append(tests, testCase{
				name: "trade-" + field + "-" + string(value), order: order,
				trades: []openalgo.Trade{row}, message: "trade " + field,
			})
		}
	}
	short := trade
	short.Quantity = "9.999999999999999999"
	tests = append(tests,
		testCase{
			name: "complete-empty", order: order,
			sentinel: errOpenAlgoFillsIncomplete, message: "incomplete",
		},
		testCase{
			name: "complete-short", order: order, trades: []openalgo.Trade{short},
			sentinel: errOpenAlgoFillsIncomplete, message: "incomplete",
		},
		testCase{
			name: "complete-excess", order: order, trades: []openalgo.Trade{trade, short},
			message: "exceed order quantity",
		},
	)
	for _, word := range []string{"cancelled", "rejected", "expired"} {
		status := order
		status.Status = word
		for _, terminal := range []openalgo.Decimal{"", "0", "8", "10"} {
			tests = append(tests, testCase{
				name: word + "-unconfirmed-" + string(terminal), order: status,
				trades: []openalgo.Trade{short}, terminal: terminal,
				sentinel: errOpenAlgoCancelledFillsUnconfirmed, message: "unconfirmed",
			})
		}
		tests = append(tests, testCase{
			name: word + "-empty-unknown-terminal", order: status,
			sentinel: errOpenAlgoCancelledFillsUnconfirmed, message: "unconfirmed",
		}, testCase{
			name: word + "-missing-positive", order: status, terminal: "1",
			sentinel: errOpenAlgoCancelledFillsUnconfirmed, message: "unconfirmed",
		}, testCase{
			name: word + "-excess", order: status, trades: []openalgo.Trade{trade, short},
			terminal: "20", message: "exceed order quantity",
		})
		for _, terminal := range []openalgo.Decimal{"bad-private", "-0.1"} {
			for _, rows := range [][]openalgo.Trade{nil, {short}} {
				tests = append(tests, testCase{
					name:  fmt.Sprintf("%s-invalid-terminal-%s-rows-%d", word, terminal, len(rows)),
					order: status, trades: rows, terminal: terminal, message: "terminal filled quantity",
				})
			}
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := openAlgoSnapshot(tt.order, tt.trades, tt.terminal)
			if err == nil || !errors.Is(err, domain.ErrUpstream) {
				t.Fatalf("error=%v, want upstream error", err)
			}
			if tt.sentinel != nil {
				if !errors.Is(err, tt.sentinel) {
					t.Fatalf("error=%v, want %v", err, tt.sentinel)
				}
			} else if errors.Is(err, errOpenAlgoFillsIncomplete) ||
				errors.Is(err, errOpenAlgoCancelledFillsUnconfirmed) {
				t.Fatalf("unexpected sentinel: %v", err)
			}
			if !strings.Contains(err.Error(), tt.message) || strings.Contains(err.Error(), "private") {
				t.Fatalf("error must name the problem without venue values: %v", err)
			}
			if !reflect.DeepEqual(got, fwtrading.Snapshot{}) {
				t.Fatalf("error returned a partial snapshot: %+v", got)
			}
		})
	}
}

func TestOpenAlgoSnapshotPermutationInvarianceAndTotals(t *testing.T) {
	for _, seed := range []int64{1, 7, 29, 61, 109, 997} {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			for sample := 0; sample < 40; sample++ {
				order := openalgo.OrderStatus{
					OrderID: "order", Symbol: "AAPL", Exchange: "NSE", Action: "BUY", Status: "complete",
				}
				var rows []openalgo.Trade
				quantity, notional := decimal.Zero, decimal.Zero
				for i, count := 0, 3+rng.Intn(8); i < count; i++ {
					qty := decimal.NewFromInt(int64(1 + rng.Intn(3))).Shift(-1)
					price := decimal.NewFromInt([]int64{2, 2, 10}[rng.Intn(3)])
					row := openalgo.Trade{
						OrderID: order.OrderID, Symbol: order.Symbol, Exchange: order.Exchange,
						Action: order.Action, Quantity: openalgo.Decimal(qty.String()),
						AveragePrice: openalgo.Decimal(price.String()),
					}
					rows = append(rows, row, row)
					row.Quantity = openalgo.Decimal(qty.String() + "0")
					row.AveragePrice = openalgo.Decimal(price.String() + ".0")
					rows = append(rows, row, openalgo.Trade{OrderID: "other"})
					quantity = quantity.Add(qty).Add(qty).Add(qty)
					notional = notional.Add(qty.Mul(price)).Add(qty.Mul(price)).Add(qty.Mul(price))
				}
				order.Quantity = openalgo.Decimal(quantity.String())
				baseline, err := openAlgoSnapshot(order, rows, "")
				if err != nil {
					t.Fatal(err)
				}
				assertOpenAlgoSnapshotChain(t, baseline, quantity)
				short := append([]openalgo.Trade(nil), rows[1:]...)
				if _, err := openAlgoSnapshot(order, short, ""); !errors.Is(err, errOpenAlgoFillsIncomplete) {
					t.Fatalf("short rows error=%v", err)
				}
				extra := append(append([]openalgo.Trade(nil), rows...), rows[0])
				if _, err := openAlgoSnapshot(order, extra, ""); err == nil ||
					!errors.Is(err, domain.ErrUpstream) || errors.Is(err, errOpenAlgoFillsIncomplete) ||
					errors.Is(err, errOpenAlgoCancelledFillsUnconfirmed) {
					t.Fatalf("extra rows error=%v", err)
				}
				for _, word := range []string{"complete", "cancelled", "rejected", "expired"} {
					order.Status = word
					terminal := openalgo.Decimal("")
					if word != "complete" {
						order.Quantity = openalgo.Decimal(quantity.Add(decimal.NewFromInt(1)).String())
						terminal = openalgo.Decimal(quantity.String())
					}
					want, err := openAlgoSnapshot(order, rows, terminal)
					if err != nil {
						t.Fatal(err)
					}
					requested := decimal.RequireFromString(string(order.Quantity))
					assertOpenAlgoSnapshotChain(t, want, requested)
					filled, value := decimal.Zero, decimal.Zero
					for _, fill := range want.Fills {
						qty := decimal.RequireFromString(fill.Quantity)
						filled = filled.Add(qty)
						value = value.Add(qty.Mul(decimal.RequireFromString(fill.Price)))
					}
					if !filled.Equal(quantity) || !value.Equal(notional) {
						t.Fatalf("fill totals differ from rows: sample=%d status=%s qty=%s value=%s, want %s/%s", sample, word, filled, value, quantity, notional)
					}
					for lookup := 0; lookup < 2; lookup++ {
						shuffled := append([]openalgo.Trade(nil), rows...)
						if lookup == 1 {
							for i := range shuffled {
								if shuffled[i].OrderID == order.OrderID {
									shuffled[i].Quantity = openalgo.Decimal(decimal.RequireFromString(string(shuffled[i].Quantity)).String())
									shuffled[i].AveragePrice = openalgo.Decimal(decimal.RequireFromString(string(shuffled[i].AveragePrice)).String())
								}
							}
						}
						rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
						before := append([]openalgo.Trade(nil), shuffled...)
						got, err := openAlgoSnapshot(order, shuffled, terminal)
						if err != nil || !reflect.DeepEqual(got, want) {
							t.Fatalf("permutation changed snapshot: sample=%d lookup=%d status=%s err=%v", sample, lookup, word, err)
						}
						if !reflect.DeepEqual(before, shuffled) {
							t.Fatal("snapshot mutated input rows")
						}
					}
				}
			}
		})
	}
}

func assertOpenAlgoSnapshotChain(t *testing.T, snapshot fwtrading.Snapshot, requested decimal.Decimal) {
	t.Helper()
	previous := decimal.Zero
	for i, fill := range snapshot.Fills {
		qty := decimal.RequireFromString(fill.Quantity)
		price := decimal.RequireFromString(fill.Price)
		cum := decimal.RequireFromString(fill.CumQuantity)
		leaves := decimal.RequireFromString(fill.LeavesQuantity)
		if !qty.IsPositive() || !price.IsPositive() || !cum.Equal(previous.Add(qty)) ||
			leaves.IsNegative() || !leaves.Equal(requested.Sub(cum)) {
			t.Fatalf("invalid snapshot chain at %d: %+v", i, fill)
		}
		if fill.Final != (i == len(snapshot.Fills)-1 && snapshot.Status == fwtrading.VenueStatusFilled) {
			t.Fatalf("invalid Final placement at %d: %+v", i, fill)
		}
		if fill.Commission != nil || fill.CommissionLeg != "" || !fill.At.IsZero() {
			t.Fatalf("invented commission or timestamp: %+v", fill)
		}
		if qty.String() != fill.Quantity || price.String() != fill.Price ||
			cum.String() != fill.CumQuantity || leaves.String() != fill.LeavesQuantity {
			t.Fatalf("noncanonical fill: %+v", fill)
		}
		previous = cum
	}
	if snapshot.FilledQuantity != previous.String() {
		t.Fatalf("filled quantity=%s, last cum=%s", snapshot.FilledQuantity, previous)
	}
}
