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

package backend

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/node"
)

type marketDataDeleteTestNode struct {
	node.Node

	deleteAssetErr error
	sink           marketdata.Sink

	deleteAssetCalls          int
	setInstanceEnabledCalls   int
	deleteInstanceCalls       int
	setInstrumentEnabledCalls int
	deleteInstrumentCalls     int
	upsertCalls               int
	instrument                domain.MarketDataInstrument
}

func (n *marketDataDeleteTestNode) CurrentMarketDataSink() marketdata.Sink {
	return n.sink
}

func (n *marketDataDeleteTestNode) DeleteAsset(
	context.Context, string, bool, domain.Caller,
) error {
	n.deleteAssetCalls++
	return n.deleteAssetErr
}

func (n *marketDataDeleteTestNode) SetMarketDataInstanceEnabled(
	context.Context, domain.ExternalID, bool, domain.Caller,
) error {
	n.setInstanceEnabledCalls++
	return nil
}

func (n *marketDataDeleteTestNode) DeleteMarketDataInstance(
	context.Context, domain.ExternalID, domain.Caller,
) error {
	n.deleteInstanceCalls++
	return nil
}

func (n *marketDataDeleteTestNode) SetMarketDataInstrumentEnabled(
	context.Context, domain.ExternalID, string, bool, domain.Caller,
) error {
	n.setInstrumentEnabledCalls++
	return nil
}

func (n *marketDataDeleteTestNode) DeleteMarketDataInstrument(
	context.Context, domain.ExternalID, string, domain.Caller,
) error {
	n.deleteInstrumentCalls++
	return nil
}

func (n *marketDataDeleteTestNode) UpsertMarketDataInstrument(
	_ context.Context, instrument domain.MarketDataInstrument, _ domain.Caller,
) error {
	n.upsertCalls++
	instrument.BaseAssetID = 1
	instrument.QuoteAssetID = 2
	n.instrument = instrument
	return nil
}

func (n *marketDataDeleteTestNode) ListMarketDataInstruments(
	_ context.Context, instance domain.ExternalID,
) ([]domain.MarketDataInstrument, error) {
	if n.instrument.Instance != instance {
		return nil, nil
	}
	return []domain.MarketDataInstrument{n.instrument}, nil
}

type marketDataDeleteTestRuntime struct {
	MarketDataRuntime

	restartErr error
	sink       marketdata.Sink

	restarts int
	stops    int
	uses     int
	pushed   int
}

func (r *marketDataDeleteTestRuntime) Restart() error {
	r.restarts++
	return r.restartErr
}

func (r *marketDataDeleteTestRuntime) Stop() {
	r.stops++
}

func (r *marketDataDeleteTestRuntime) UseSink(sink marketdata.Sink) error {
	r.uses++
	r.sink = sink
	return nil
}

func (r *marketDataDeleteTestRuntime) PushManual(
	context.Context, string, domain.MarketDataInstrument,
) error {
	r.pushed++
	return nil
}

type marketDataDeleteTestSink struct{}

func (marketDataDeleteTestSink) Push(marketdata.QuoteUpdate) error {
	return nil
}

func newMarketDataDeleteTestService(
	t *testing.T,
	n *marketDataDeleteTestNode,
	md MarketDataRuntime,
) *Service {
	t.Helper()
	return &Service{node: n, md: md}
}

func TestServiceDeleteAssetForceRestartsMarketData(t *testing.T) {
	t.Parallel()

	n := &marketDataDeleteTestNode{sink: marketDataDeleteTestSink{}}
	md := &marketDataDeleteTestRuntime{}
	svc := newMarketDataDeleteTestService(t, n, md)

	if err := svc.DeleteAsset(context.Background(), "USD", true); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if n.deleteAssetCalls != 1 {
		t.Fatalf("asset delete calls = %d, want 1", n.deleteAssetCalls)
	}
	if md.stops != 1 || md.restarts != 1 || md.uses != 1 {
		t.Fatalf(
			"market-data calls stop=%d restart=%d use-sink=%d, want 1 each",
			md.stops, md.restarts, md.uses,
		)
	}
}

// A non-forced delete restarts feeds with no instrument referencing the asset.
func TestServiceDeleteAssetWithoutForceRestartsMarketData(t *testing.T) {
	t.Parallel()

	n := &marketDataDeleteTestNode{sink: marketDataDeleteTestSink{}}
	md := &marketDataDeleteTestRuntime{}
	svc := newMarketDataDeleteTestService(t, n, md)

	if err := svc.DeleteAsset(context.Background(), "USD", false); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if n.deleteAssetCalls != 1 {
		t.Fatalf("asset delete calls = %d, want 1", n.deleteAssetCalls)
	}
	if md.stops != 1 || md.restarts != 1 || md.uses != 1 {
		t.Fatalf(
			"market-data calls stop=%d restart=%d use-sink=%d, want 1 each",
			md.stops, md.restarts, md.uses,
		)
	}
}

func TestServiceMarketDataConfigurationMutationsReachNodeWithoutRestartingMarketData(
	t *testing.T,
) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(*Service) error
		calls  func(*marketDataDeleteTestNode) int
	}{
		{
			name: "set instance enabled",
			mutate: func(s *Service) error {
				return s.SetMarketDataInstanceEnabled(
					context.Background(), "instance-1", true,
				)
			},
			calls: func(n *marketDataDeleteTestNode) int {
				return n.setInstanceEnabledCalls
			},
		},
		{
			name: "delete instance",
			mutate: func(s *Service) error {
				return s.DeleteMarketDataInstance(context.Background(), "instance-1")
			},
			calls: func(n *marketDataDeleteTestNode) int {
				return n.deleteInstanceCalls
			},
		},
		{
			name: "set instrument enabled",
			mutate: func(s *Service) error {
				return s.SetMarketDataInstrumentEnabled(
					context.Background(), "instance-1", "AAPL/USD", true,
				)
			},
			calls: func(n *marketDataDeleteTestNode) int {
				return n.setInstrumentEnabledCalls
			},
		},
		{
			name: "delete instrument",
			mutate: func(s *Service) error {
				return s.DeleteMarketDataInstrument(
					context.Background(), "instance-1", "AAPL/USD",
				)
			},
			calls: func(n *marketDataDeleteTestNode) int {
				return n.deleteInstrumentCalls
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			n := &marketDataDeleteTestNode{sink: marketDataDeleteTestSink{}}
			md := &marketDataDeleteTestRuntime{}
			svc := newMarketDataDeleteTestService(t, n, md)
			done := make(chan error, 1)
			go func() {
				done <- test.mutate(svc)
			}()

			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("market-data configuration mutation: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("market-data configuration mutation deadlocked")
			}
			if got := test.calls(n); got != 1 {
				t.Fatalf("node calls = %d, want 1", got)
			}
			if md.stops != 0 || md.restarts != 0 || md.uses != 0 {
				t.Fatalf(
					"market-data calls stop=%d restart=%d use-sink=%d, want 0 each",
					md.stops, md.restarts, md.uses,
				)
			}
		})
	}
}

func TestServiceUpsertMarketDataInstrumentDoesNotRestartMarketData(t *testing.T) {
	t.Parallel()

	n := &marketDataDeleteTestNode{}
	md := &marketDataDeleteTestRuntime{}
	svc := newMarketDataDeleteTestService(t, n, md)

	err := svc.UpsertMarketDataInstrument(context.Background(), domain.MarketDataInstrument{
		Instance:       "instance-1",
		ExternalSymbol: "AAPL/USD",
		BaseAsset:      "AAPL",
		QuoteAsset:     "USD",
		Enabled:        true,
	})
	if err != nil {
		t.Fatalf("UpsertMarketDataInstrument: %v", err)
	}
	if n.upsertCalls != 1 || md.pushed != 1 {
		t.Fatalf("upsert calls=%d pushed=%d, want 1 each", n.upsertCalls, md.pushed)
	}
	if md.stops != 0 || md.restarts != 0 || md.uses != 0 {
		t.Fatalf(
			"market-data calls stop=%d restart=%d use-sink=%d, want 0 each",
			md.stops, md.restarts, md.uses,
		)
	}
}

func TestServiceDeleteMarketDataRestartFailureReportsDurableDeletion(t *testing.T) {
	t.Parallel()

	restartErr := errors.New("feed startup failed")
	n := &marketDataDeleteTestNode{sink: marketDataDeleteTestSink{}}
	md := &marketDataDeleteTestRuntime{restartErr: restartErr}
	svc := newMarketDataDeleteTestService(t, n, md)

	err := svc.DeleteAsset(context.Background(), "USD", true)
	if !errors.Is(err, restartErr) {
		t.Fatalf("DeleteAsset error = %v, want restart error", err)
	}
	if !strings.Contains(err.Error(), "deletion is durable") ||
		!strings.Contains(err.Error(), "feeds need attention") {
		t.Fatalf("DeleteAsset error = %q, want durable deletion warning", err)
	}
	if n.deleteAssetCalls != 1 || md.stops != 1 || md.restarts != 1 {
		t.Fatalf(
			"asset delete calls=%d stop=%d restart=%d, want 1 each",
			n.deleteAssetCalls, md.stops, md.restarts,
		)
	}
}

func TestServiceDeleteMarketDataRestartWithNoRuntimeIsNoOp(t *testing.T) {
	t.Parallel()

	n := &marketDataDeleteTestNode{}
	svc := newMarketDataDeleteTestService(t, n, nil)

	if err := svc.DeleteAsset(context.Background(), "USD", true); err != nil {
		t.Fatalf("DeleteAsset: %v", err)
	}
	if n.deleteAssetCalls != 1 {
		t.Fatalf("asset delete calls = %d, want 1", n.deleteAssetCalls)
	}
}
