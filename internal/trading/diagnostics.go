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

import "go.openpit.dev/officer/framework/health"

const venueStreamUnparsableTitle = "Unreadable data on the venue stream"

// venueStreamUnparsable builds the unparsable_data diagnostic of one stream site.
func venueStreamUnparsable(level, detail, remediation string) *health.Diagnostic {
	return &health.Diagnostic{
		Level: level, Code: health.CodeUnparsableData,
		Kind: health.DiagKindProvider, Title: venueStreamUnparsableTitle,
		Detail: detail, Remediation: remediation,
	}
}

const (
	venueStreamSkipped = "Officer skipped the message and kept reading the " +
		"stream. This is a venue or Officer issue, not your configuration " +
		"- please report it if it repeats."
	venueStreamDropped = "Officer dropped the update; a fill it carried is not " +
		"reconciled until the next stream reconnect. This is a venue or " +
		"Officer issue, not your configuration - please report it."
	venueStreamReconciled = "Officer reconciles the order from the venue's " +
		"order and fill history instead. This is a venue or Officer issue, " +
		"not your configuration - please report it if it repeats."
	venueStreamNotBooked = "Officer did not book the update. An order Officer " +
		"still has open is reconciled from the venue's order and fill " +
		"history; an excess on an order Officer has already finished is " +
		"not booked anywhere. Compare the account's position at the venue " +
		"with Officer's. This is a venue issue, not your configuration."
)

// An unparsable_data site reports once per connection, so a venue
// sending malformed frames cannot turn every frame into a report; a
// site's detail is fixed and identifies it. An inconsistent_data
// diagnostic reports every time: each one is a fill the venue reports
// beyond the order quantity, and one on an order Officer has already
// finished leaves no other trace.
func venueStreamReporter(reportDiag health.DiagnosticReporter) func(*health.Diagnostic) {
	reported := make(map[string]bool)
	return func(diag *health.Diagnostic) {
		if diag.Code == health.CodeUnparsableData {
			if reported[diag.Detail] {
				return
			}
			reported[diag.Detail] = true
		}
		reportDiag(*diag)
	}
}

// venueLookupUnparsable builds the unparsable_data diagnostic of an order
// lookup site that read a value Officer does not know. The detail is fixed per
// site and never embeds the value.
func venueLookupUnparsable(detail string) health.Diagnostic {
	return health.Diagnostic{
		Level: health.DiagError, Code: health.CodeUnparsableData,
		Kind: health.DiagKindProvider, Title: "Unknown order data from the venue",
		Detail: detail,
		Remediation: "Officer keeps the order pending and retries the " +
			"lookup; the order is not reconciled until the venue reports " +
			"data Officer knows. This is a venue or Officer issue, not your " +
			"configuration - please report it.",
	}
}
