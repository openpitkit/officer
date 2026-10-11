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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/health"
	"go.openpit.dev/officer/framework/trading"
)

func TestConnectionStreamFailureDegrades(t *testing.T) {
	f := newFixture(t)
	f.start()
	report, _ := f.venue.reporters(t)
	id := f.connection.ExternalID
	report(false, "stream refused")
	report(false, "stream refused")
	status := f.runtime.ConnectionStatuses()[id]
	if status.State != trading.ConnectionDegraded || status.Error != "stream refused" {
		t.Fatalf("failed stream status = %+v", status)
	}
	if len(status.Diagnostics) != 1 {
		t.Fatalf("repeated stream failure diagnostics = %+v, want one", status.Diagnostics)
	}
	diag := status.Diagnostics[0]
	if diag.Level != health.DiagError || diag.Code != health.CodeConnectionError ||
		diag.Kind != health.DiagKindEnvironment || diag.Detail != "stream refused" ||
		diag.Title == "" || diag.Remediation == "" || len(diag.Actions) != 0 {
		t.Fatalf("stream failure diagnostic = %+v", diag)
	}
	if logged := strings.Count(f.logs.text(), `"detail":"stream refused"`); logged != 1 {
		t.Fatalf("repeated stream failure logged %d times, want once", logged)
	}
	report(true, "")
	status = f.runtime.ConnectionStatuses()[id]
	if status.State != trading.ConnectionRunning || status.Error != "" || len(status.Diagnostics) != 1 {
		t.Fatalf("recovered stream status = %+v, want running with the failure kept", status)
	}
}

func TestConnectionStreamFailureWithoutMessageStaysDegraded(t *testing.T) {
	f := newFixture(t)
	f.start()
	report, _ := f.venue.reporters(t)
	id := f.connection.ExternalID
	report(false, "stream refused")
	report(false, "")
	if status := f.runtime.ConnectionStatuses()[id]; status.State != trading.ConnectionDegraded || status.Error != "" {
		t.Fatalf("status after a failure without a message = %+v, want degraded with that empty message", status)
	}
	report(true, "")
	if status := f.runtime.ConnectionStatuses()[id]; status.State != trading.ConnectionRunning || status.Error != "" {
		t.Fatalf("status after ok = %+v, want running", status)
	}
}

func TestConnectionStatusKeepsReportDuringStart(t *testing.T) {
	f := newFixture(t)
	f.venue.onSubscribe = func() {
		// The connector's stream goroutine reports before Subscribe returns,
		// so before Start publishes the worker.
		report, _ := f.venue.reporters(t)
		reported := make(chan struct{})
		go func() { defer close(reported); report(false, "stream refused") }()
		<-reported
	}
	f.start()
	status := f.runtime.ConnectionStatuses()[f.connection.ExternalID]
	if status.State != trading.ConnectionDegraded || status.Error != "stream refused" || len(status.Diagnostics) != 1 {
		t.Fatalf("status after a report during Start = %+v, want degraded", status)
	}
}

func TestConnectionDegradedStillSends(t *testing.T) {
	f := newFixture(t)
	f.start()
	report, _ := f.venue.reporters(t)
	report(false, "stream refused")
	if status := f.runtime.ConnectionStatuses()[f.connection.ExternalID]; status.State != trading.ConnectionDegraded {
		t.Fatalf("status = %+v, want degraded", status)
	}
	if err := f.runtime.PreCheck(testCtx, f.draft("1"), f.dest); err != nil {
		t.Fatalf("degraded PreCheck = %v", err)
	}
	link, err := f.runtime.Send(testCtx, f.submit("1").ExternalID, f.dest)
	if err != nil || link.VenueOrderID == "" {
		t.Fatalf("degraded Send = %+v, %v", link, err)
	}
}

func TestConnectionUnavailableWinsOverStreamFailure(t *testing.T) {
	f := newFixture(t)
	f.venue.subscribeErr = errors.New("authentication failed")
	f.start()
	report, _ := f.venue.reporters(t)
	report(false, "stream refused")
	status := f.runtime.ConnectionStatuses()[f.connection.ExternalID]
	if status.State != trading.ConnectionUnavailable || status.Error != "authentication failed" {
		t.Fatalf("unavailable worker with a stream failure = %+v", status)
	}
	if len(status.Diagnostics) != 2 || status.Diagnostics[1].Detail != "stream refused" {
		t.Fatalf("diagnostics = %+v, want the Subscribe failure, then the stream failure", status.Diagnostics)
	}
}

func TestConnectionReportedDiagnostic(t *testing.T) {
	f := newFixture(t)
	f.start()
	_, report := f.venue.reporters(t)
	want := health.Diagnostic{
		At:          time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Actions:     []health.DiagnosticAction{{Type: health.ActionOpenDocs}},
		Level:       health.DiagWarn,
		Code:        health.CodeUnparsableData,
		Kind:        health.DiagKindProvider,
		Title:       "Unparsable venue message",
		Detail:      "unexpected field",
		Remediation: "Report the message to the venue.",
		Instrument:  "AAPL",
	}
	report(want)
	status := f.runtime.ConnectionStatuses()[f.connection.ExternalID]
	if status.State != trading.ConnectionRunning || len(status.Diagnostics) != 1 ||
		!reflect.DeepEqual(status.Diagnostics[0], want) {
		t.Fatalf("reported diagnostic status = %+v, want running with %+v", status, want)
	}
}

func TestConnectionUnavailableDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		failure, code, kind string
		gate                error
	}{
		{"build", health.CodeInvalidProviderConfig, health.DiagKindConfig, domain.ErrUpstream},
		// Sends check the provider before the worker, as before.
		{"unsupported", health.CodeUnsupportedProvider, health.DiagKindConfig, domain.ErrInvalid},
		{"subscribe", health.CodeConnectionError, health.DiagKindEnvironment, domain.ErrUpstream},
		{"nil_channel", health.CodeInternalError, health.DiagKindProvider, domain.ErrUpstream},
		{"closed", health.CodeConnectionError, health.DiagKindProvider, domain.ErrUpstream},
	} {
		t.Run(tc.failure, func(t *testing.T) {
			f := newFixture(t)
			cause := errors.New("authentication failed")
			id := f.connection.ExternalID
			switch tc.failure {
			case "build":
				must(t, f.registry.Register(trading.Provider{Type: "broken",
					Build: func(domain.TradingConnection) (trading.Connector, error) { return nil, cause }}))
				id = f.addConnection("broken", "broken", true).ExternalID
			case "unsupported":
				id = f.addConnection("unregistered", "unregistered", true).ExternalID
			case "subscribe":
				f.venue.subscribeErr = cause
			case "nil_channel":
				f.venue.nilEvents = true
			}
			f.start()
			if tc.failure == "closed" {
				f.venue.mu.Lock()
				connector := f.venue.current
				f.venue.mu.Unlock()
				connector.closeEvents()
				waitFor(t, func() bool {
					return f.runtime.ConnectionStatuses()[id].State == trading.ConnectionUnavailable
				}, "closed event stream status")
			}
			status := f.runtime.ConnectionStatuses()[id]
			if status.State != trading.ConnectionUnavailable || len(status.Diagnostics) != 1 {
				t.Fatalf("%s failure status = %+v", tc.failure, status)
			}
			diag := status.Diagnostics[0]
			if diag.Level != health.DiagError || diag.Code != tc.code || diag.Kind != tc.kind ||
				diag.Detail != status.Error || diag.Title == "" || diag.Remediation == "" ||
				len(diag.Actions) != 0 {
				t.Fatalf("%s failure diagnostic = %+v", tc.failure, diag)
			}
			dest := domain.TradingDestination{Connection: id, Route: "{}"}
			if err := f.runtime.PreCheck(testCtx, f.draft("1"), dest); !errors.Is(err, tc.gate) {
				t.Fatalf("%s failure PreCheck = %v", tc.failure, err)
			}
			_, err := f.runtime.Send(testCtx, f.submit("1").ExternalID, dest)
			if !errors.Is(err, tc.gate) || !errors.Is(err, trading.ErrNotSent) {
				t.Fatalf("%s failure Send = %v", tc.failure, err)
			}
		})
	}
}

func TestConnectionStatusesCopyDiagnostics(t *testing.T) {
	f := newFixture(t)
	f.start()
	_, report := f.venue.reporters(t)
	report(health.Diagnostic{Level: health.DiagWarn, Code: health.CodeNoData,
		Kind: health.DiagKindProvider, Title: "No venue data", Detail: "quiet",
		Actions: []health.DiagnosticAction{{Type: health.ActionOpenDocs, Target: "https://venue.test/docs"}}})
	id := f.connection.ExternalID
	diag := &f.runtime.ConnectionStatuses()[id].Diagnostics[0]
	diag.Detail = "changed by the caller"
	diag.Actions[0].Target = "changed by the caller"
	got := f.runtime.ConnectionStatuses()[id].Diagnostics
	if len(got) != 1 || got[0].Detail != "quiet" {
		t.Fatalf("caller's diagnostic change reached the next snapshot: %+v", got)
	}
	if got[0].Actions[0].Target != "https://venue.test/docs" {
		t.Fatalf("caller's action change reached the next snapshot: %+v", got[0].Actions)
	}
}

func TestConnectionStatusIgnoresPreviousGeneration(t *testing.T) {
	f := newFixture(t)
	f.start()
	staleStatus, staleDiagnostic := f.venue.reporters(t)
	must(t, f.runtime.Restart())
	staleStatus(false, "stale stream refused")
	staleDiagnostic(health.Diagnostic{Level: health.DiagWarn, Code: health.CodeNoData,
		Kind: health.DiagKindProvider, Title: "No venue data", Detail: "stale venue data"})
	id := f.connection.ExternalID
	if status := f.runtime.ConnectionStatuses()[id]; status.State != trading.ConnectionRunning || len(status.Diagnostics) != 0 {
		t.Fatalf("previous generation's reports changed the new status: %+v", status)
	}
	if strings.Contains(f.logs.text(), "stale stream refused") {
		t.Fatal("previous generation's status report was logged")
	}
	if strings.Contains(f.logs.text(), "stale venue data") {
		t.Fatal("previous generation's diagnostic was logged")
	}
	report, _ := f.venue.reporters(t)
	report(false, "stream refused")
	if status := f.runtime.ConnectionStatuses()[id]; status.State != trading.ConnectionDegraded {
		t.Fatalf("new generation's report did not degrade it: %+v", status)
	}
}

func TestConnectionStatusConcurrentReports(t *testing.T) {
	f := newFixture(t)
	f.start()
	report, diagnose := f.venue.reporters(t)
	id := f.connection.ExternalID
	var reporters sync.WaitGroup
	for range 4 {
		reporters.Add(1)
		go func() {
			defer reporters.Done()
			for i := range 50 {
				report(i%2 == 0, "stream refused")
				diagnose(health.Diagnostic{Level: health.DiagWarn, Code: health.CodeNoData,
					Kind: health.DiagKindProvider, Title: "No venue data", Detail: "quiet"})
			}
		}()
	}
	done := make(chan struct{})
	go func() { reporters.Wait(); close(done) }()
	for {
		for _, diag := range f.runtime.ConnectionStatuses()[id].Diagnostics {
			if diag.At.IsZero() {
				t.Fatalf("snapshot diagnostic without a time: %+v", diag)
			}
		}
		select {
		case <-done:
			if n := len(f.runtime.ConnectionStatuses()[id].Diagnostics); n == 0 || n > 20 {
				t.Fatalf("diagnostics after concurrent reports = %d, want 1 to 20", n)
			}
			return
		default:
		}
	}
}
