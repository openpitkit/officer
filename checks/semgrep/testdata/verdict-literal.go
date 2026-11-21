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

package fixture

import "go.openpit.dev/officer/framework/domain"

func verdictLiterals(p, res struct{ Verdict string }, verdict string) {
	// ruleid: verdict-literal
	if p.Verdict == "accept" {
	}
	// ruleid: verdict-literal
	if res.Verdict != "reject" {
	}
	// ruleid: verdict-literal
	if "review" == p.Verdict {
	}
	// ruleid: verdict-literal
	if "accept" != res.Verdict {
	}
	// ruleid: verdict-literal
	if verdict == "accept" {
	}
	// ruleid: verdict-literal
	if verdict != "reject" {
	}
	// ruleid: verdict-literal
	if "review" == verdict {
	}
	// ruleid: verdict-literal
	if "accept" != verdict {
	}
	// ruleid: verdict-literal
	p.Verdict = "accept"
	// ruleid: verdict-literal
	verdict = "review"
	// ruleid: verdict-literal
	res = struct{ Verdict string }{Verdict: "reject"}
	// ruleid: verdict-literal
	switch p.Verdict {
	case "accept":
	case "reject", "review":
	default:
	}
	// ruleid: verdict-literal
	switch verdict {
	case "reject":
	default:
	}
	// ruleid: verdict-literal
	switch res.Verdict {
	case "reject", "review":
	}
	// ruleid: verdict-literal
	switch verdict {
	case string(domain.VerdictAccept):
	case "review":
	}
	// ok: verdict-literal
	p.Verdict = ""
	// ok: verdict-literal
	verdict = ""
	// ok: verdict-literal
	if p.Verdict != "" {
	}
	// ok: verdict-literal
	if "" == verdict {
	}
	// ok: verdict-literal
	res = struct{ Verdict string }{Verdict: ""}
	// ok: verdict-literal
	switch verdict {
	case "":
	}
	// ruleid: verdict-literal
	if verdict == `accept` {
	}
	// ok: verdict-literal
	if verdict == `` {
	}
	// ok: verdict-literal
	p.Verdict = string(domain.VerdictAccept)
	// ok: verdict-literal
	if domain.Verdict(verdict) == domain.VerdictReview {
	}
	// ok: verdict-literal
	switch domain.Verdict(verdict) {
	case domain.VerdictAccept:
	case domain.VerdictReject:
	case domain.VerdictReview:
	}
	// ok: verdict-literal
	status := "accept"
	// ok: verdict-literal
	if status == "reject" {
	}
	// ok: verdict-literal
	switch status {
	case "review":
	}
	// ok: verdict-literal
	// p.Verdict = "accept"
	// ok: verdict-literal
	/* verdict = "review" */
}

func declareVerdict() {
	// ruleid: verdict-literal
	verdict := "accept"
	_ = verdict
}

func emptyVerdict() {
	// ok: verdict-literal
	verdict := ""
	_ = verdict
}

func upperVerdict() {
	// ruleid: verdict-literal
	Verdict := "accept"
	// ruleid: verdict-literal
	Verdict = "reject"
	// ruleid: verdict-literal
	if Verdict == "review" {
	}
	// ruleid: verdict-literal
	switch Verdict {
	case "review":
	}
}

func constantVerdict() {
	// ok: verdict-literal
	verdict := domain.VerdictAccept
	// ok: verdict-literal
	verdict = domain.VerdictReject
	// ok: verdict-literal
	p := struct{ Verdict domain.Verdict }{Verdict: domain.VerdictReview}
	// ok: verdict-literal
	if p.Verdict == domain.VerdictReview {
	}
	// ok: verdict-literal
	switch verdict {
	case domain.VerdictAccept:
	case domain.VerdictReject:
	case domain.VerdictReview:
	}
}
