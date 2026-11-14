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

package trading_test

import (
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/marketdata"
	"go.openpit.dev/officer/framework/trading"
)

func TestRegistryExecutionPolicyCapability(t *testing.T) {
	for _, supported := range []bool{false, true} {
		r := trading.NewRegistry()
		must(t, r.Register(trading.Provider{Type: "custom", ExecutionPolicy: supported, Build: func(trading.BuildContext) (trading.Connector, error) { return nil, nil }}))
		got, err := r.SupportsExecutionPolicy("custom")
		must(t, err)
		if got != supported {
			t.Fatalf("policy capability=%v, want %v", got, supported)
		}
	}
}

func TestRegistryUnknownProviderCapability(t *testing.T) {
	class, err := trading.NewRegistry().SupportsExecutionPolicy("missing-provider")
	if class || !errors.Is(err, domain.ErrTradingProviderUnavailable) || !strings.Contains(err.Error(), "missing-provider") {
		t.Fatalf("policy capability=%v err=%v", class, err)
	}
}

type testQuoteSource struct{}

func (*testQuoteSource) QuoteSnapshots() []marketdata.QuoteSnapshot { return nil }

func TestRuntimePassesBuildContext(t *testing.T) {
	f := newFixture(t)
	quotes := &testQuoteSource{}
	var builds []trading.BuildContext
	must(t, f.registry.Register(trading.Provider{
		Type: "fake", ExecutionPolicy: false,
		Build: func(ctx trading.BuildContext) (trading.Connector, error) {
			builds = append(builds, ctx)
			return f.venue.build(ctx)
		},
	}))
	runtime, err := trading.NewRuntime(f.registry, f.st, quotes, f.sink, f.adjustmentSink, f.feeSkipSink, slog.Default())
	must(t, err)
	t.Cleanup(runtime.Stop)
	draft := f.connection
	draft.ExternalID = ""
	if _, err := runtime.CheckAccount(testCtx, draft); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("CheckAccount: %v", err)
	}
	if _, err := runtime.VerifySymbol(testCtx, draft, "AAPL"); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("VerifySymbol: %v", err)
	}
	if _, err := runtime.SearchSymbols(testCtx, draft, "AA"); !errors.Is(err, trading.ErrUnsupported) {
		t.Fatalf("SearchSymbols: %v", err)
	}
	must(t, runtime.Start(testCtx))
	must(t, runtime.Restart())
	if len(builds) != 5 {
		t.Fatalf("builds=%d, want three helpers, start and restart", len(builds))
	}
	for i, build := range builds {
		want := f.connection
		if i < 3 {
			want = draft
		}
		if !reflect.DeepEqual(build.Connection, want) || build.Quotes != quotes || build.Store != f.st {
			t.Fatalf("build %d lost context: %+v", i, build)
		}
	}
}
