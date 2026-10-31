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
	"testing"

	"go.openpit.dev/officer/framework/health"
)

func TestVenueStreamReporter(t *testing.T) {
	var reported []health.Diagnostic
	report := venueStreamReporter(func(diag health.Diagnostic) {
		reported = append(reported, diag)
	})
	first := venueStreamUnparsable(health.DiagWarn, "first", venueStreamSkipped)
	second := venueStreamUnparsable(health.DiagWarn, "second", venueStreamSkipped)
	inconsistent := &health.Diagnostic{Code: health.CodeInconsistentData, Detail: "first"}
	report(first)
	report(first)
	report(second)
	report(inconsistent)
	report(inconsistent)
	if len(reported) != 4 {
		t.Fatalf("diagnostics = %v, want first, second, inconsistent twice", reported)
	}
	for i, want := range []*health.Diagnostic{first, second, inconsistent, inconsistent} {
		if got := reported[i]; got.Code != want.Code || got.Detail != want.Detail {
			t.Fatalf("diagnostic %d = %v, want %v", i, got, want)
		}
	}
	report = venueStreamReporter(func(diag health.Diagnostic) {
		reported = append(reported, diag)
	})
	report(first)
	if len(reported) != 5 {
		t.Fatal("new connection did not report the same detail again")
	}
}
