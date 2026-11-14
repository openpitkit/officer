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

package dto_test

import (
	"encoding/json"
	"errors"
	"go.openpit.dev/officer/framework/domain"
	httpdto "go.openpit.dev/officer/framework/web/httpapi/dto"
	"reflect"
	"testing"
)

func TestFromTestExecutionPolicyDTO(t *testing.T) {
	cases := []struct{ name, rules, pointer, constraint string }{
		{"missing rules", `{}`, "/rules", "required"},
		{"null rules", `{"rules":null}`, "/rules", "required"},
		{"empty rules", `{"rules":[]}`, "", ""},
		{"mask missing", `{"rules":[{"mode":"execute","priceVariant":"normal"}]}`, "/rules/0/mask/baseAsset", "required"},
		{"mask null", `{"rules":[{"mask":null,"mode":"execute","priceVariant":"normal"}]}`, "/rules/0/mask/baseAsset", "required"},
		{"quote missing", `{"rules":[{"mask":{"baseAsset":"A*"},"mode":"execute","priceVariant":"normal"}]}`, "/rules/0/mask/quoteAsset", "required"},
		{"base format", `{"rules":[{"mask":{"baseAsset":" A","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal"}]}`, "/rules/0/mask/baseAsset", "format"},
	}
	check := func(t *testing.T, raw, pointer, constraint string) {
		t.Helper()
		var dto httpdto.TestExecutionPolicyDTO
		if err := json.Unmarshal([]byte(raw), &dto); err != nil {
			t.Fatal(err)
		}
		policy, err := httpdto.FromTestExecutionPolicyDTO(dto)
		if constraint == "" {
			if err != nil {
				t.Fatal(err)
			}
			if policy.Rules == nil {
				t.Fatal("rules became nil")
			}
			return
		}
		if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != pointer || domain.ValidationConstraint(err) != constraint {
			t.Fatalf("error=%v pointer=%q constraint=%q", err, domain.ValidationPointer(err), domain.ValidationConstraint(err))
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { check(t, tc.rules, tc.pointer, tc.constraint) })
	}
	mask := `"mask":{"baseAsset":"A*","quoteAsset":"USD"}`
	rules := []struct{ name, fields, pointer, constraint string }{
		{"mode required", `"priceVariant":"normal"`, "mode", "required"},
		{"mode enum", `"mode":"other","priceVariant":"normal"`, "mode", "enum"},
		{"split fills required", `"mode":"split","priceVariant":"normal"`, "fills", "required"},
		{"split fills low", `"mode":"split","priceVariant":"normal","fills":1`, "fills", "enum"},
		{"split fills high", `"mode":"split","priceVariant":"normal","fills":4`, "fills", "enum"},
		{"split fills two", `"mode":"split","priceVariant":"normal","fills":2`, "", ""},
		{"split fills three", `"mode":"split","priceVariant":"normal","fills":3`, "", ""},
		{"rest wait required", `"mode":"rest","priceVariant":"normal"`, "wait", "required"},
		{"wait kind required", `"mode":"rest","priceVariant":"normal","wait":{}`, "wait/kind", "required"},
		{"wait kind enum", `"mode":"rest","priceVariant":"normal","wait":{"kind":"other"}`, "wait/kind", "enum"},
		{"seconds required", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite"}`, "wait/seconds", "required"},
		{"seconds zero", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":0}`, "wait/seconds", "range"},
		{"seconds one", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":1}`, "", ""},
		{"seconds maximum", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":9223372036}`, "", ""},
		{"seconds overflow", `"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":9223372037}`, "wait/seconds", "range"},
		{"infinite", `"mode":"rest","wait":{"kind":"infinite"}`, "", ""},
		{"infinite seconds", `"mode":"rest","wait":{"kind":"infinite","seconds":1}`, "wait/seconds", "forbidden"},
		{"infinite variant", `"mode":"rest","wait":{"kind":"infinite"},"priceVariant":"normal"`, "priceVariant", "forbidden"},
		{"infinite empty variant", `"mode":"rest","wait":{"kind":"infinite"},"priceVariant":""`, "priceVariant", "forbidden"},
	}
	for _, tc := range rules {
		t.Run(tc.name, func(t *testing.T) {
			pointer := ""
			if tc.pointer != "" {
				pointer = "/rules/0/" + tc.pointer
			}
			check(t, `{"rules":[{`+mask+`,`+tc.fields+`}]}`, pointer, tc.constraint)
		})
	}
	for _, mode := range []string{"execute", "reject_even", "split", "rest"} {
		fields := `"mode":"` + mode + `"`
		if mode == "split" {
			fields += `,"fills":2`
		}
		if mode == "rest" {
			fields += `,"wait":{"kind":"finite","seconds":1}`
		}
		for _, variant := range []struct{ name, field, constraint string }{{"missing", "", "required"}, {"empty", `,"priceVariant":""`, "required"}, {"unknown", `,"priceVariant":"other"`, "enum"}, {"normal", `,"priceVariant":"normal"`, ""}, {"best", `,"priceVariant":"best"`, ""}, {"worst", `,"priceVariant":"worst"`, ""}} {
			t.Run(mode+" variant "+variant.name, func(t *testing.T) {
				pointer := ""
				if variant.constraint != "" {
					pointer = "/rules/0/priceVariant"
				}
				check(t, `{"rules":[{`+mask+`,`+fields+variant.field+`}]}`, pointer, variant.constraint)
			})
		}
		if mode != "split" {
			t.Run(mode+" forbidden fills", func(t *testing.T) {
				check(t, `{"rules":[{`+mask+`,`+fields+`,"priceVariant":"normal","fills":2}]}`, "/rules/0/fills", "forbidden")
			})
		}
		if mode != "rest" {
			t.Run(mode+" forbidden wait", func(t *testing.T) {
				check(t, `{"rules":[{`+mask+`,`+fields+`,"priceVariant":"normal","wait":{"kind":"infinite"}}]}`, "/rules/0/wait", "forbidden")
			})
		}
	}
	check(t, `{"rules":[{`+mask+`,"mode":"execute","priceVariant":"normal"},{"mask":{"baseAsset":"","quoteAsset":"USD"}}]}`, "/rules/1/mask/baseAsset", "required")
}

func TestTestExecutionPolicyDTORejectsNullMembers(t *testing.T) {
	for _, field := range []struct{ name, member string }{
		{"priceVariant", `"priceVariant":null`},
		{"fills", `"fills":null`},
		{"wait", `"wait":null`},
		{"wait/seconds", `"wait":{"kind":"infinite","seconds":null}`},
	} {
		t.Run(field.member, func(t *testing.T) {
			var dto httpdto.TestExecutionPolicyDTO
			err := json.Unmarshal([]byte(`{"rules":[{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"execute",`+field.member+`}]}`), &dto)
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/rules/0/"+field.name || domain.ValidationConstraint(err) != "type" {
				t.Fatalf("error=%v pointer=%q constraint=%q", err, domain.ValidationPointer(err), domain.ValidationConstraint(err))
			}
		})
	}
}

func TestTestExecutionPolicyDTORoundTrip(t *testing.T) {
	raw := `{"rules":[{"mask":{"baseAsset":"A*","quoteAsset":"USD"},"mode":"split","priceVariant":"worst","fills":3},{"mask":{"baseAsset":"*","quoteAsset":"*"},"mode":"rest","wait":{"kind":"infinite"}},{"mask":{"baseAsset":"A*","quoteAsset":"USD"},"mode":"split","priceVariant":"worst","fills":3}]}`
	var dto httpdto.TestExecutionPolicyDTO
	if err := json.Unmarshal([]byte(raw), &dto); err != nil {
		t.Fatal(err)
	}
	policy, err := httpdto.FromTestExecutionPolicyDTO(dto)
	if err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewExternalID()
	if err != nil {
		t.Fatal(err)
	}
	out := httpdto.ToTestExecutionPolicyStateDTO(id, domain.TestExecutionPolicyState{Revision: ^uint64(0), Policy: policy})
	if out.Connection != id.String() || out.Revision != "18446744073709551615" || !reflect.DeepEqual(out.TestExecutionPolicyDTO, dto) {
		t.Fatalf("round trip: %+v", out)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded httpdto.TestExecutionPolicyStateDTO
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, out) {
		t.Fatalf("snapshot round trip: %+v", decoded)
	}
	empty := httpdto.ToTestExecutionPolicyStateDTO(id, domain.TestExecutionPolicyState{Revision: 1, Policy: domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}})
	if empty.Rules == nil || len(empty.Rules) != 0 {
		t.Fatalf("empty rules: %+v", empty)
	}
}

func TestTestExecutionPolicyDTOOwnsPointers(t *testing.T) {
	for _, direction := range []string{"from", "to"} {
		t.Run(direction, func(t *testing.T) {
			raw := `{"rules":[{"mask":{"baseAsset":"A*","quoteAsset":"USD"},"mode":"split","priceVariant":"normal","fills":2},{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"rest","priceVariant":"normal","wait":{"kind":"finite","seconds":10}}]}`
			var dto httpdto.TestExecutionPolicyDTO
			if err := json.Unmarshal([]byte(raw), &dto); err != nil {
				t.Fatal(err)
			}
			policy, err := httpdto.FromTestExecutionPolicyDTO(dto)
			if err != nil {
				t.Fatal(err)
			}
			if direction == "to" {
				id, err := domain.NewExternalID()
				if err != nil {
					t.Fatal(err)
				}
				dto = httpdto.ToTestExecutionPolicyStateDTO(id, domain.TestExecutionPolicyState{Revision: 1, Policy: policy}).TestExecutionPolicyDTO
			}
			*dto.Rules[0].Fills = 3
			*dto.Rules[1].Wait.Seconds = 20
			if got := *policy.Rules[0].Fills; got != 2 {
				t.Errorf("DTO mutation changed domain fills: got %d, want 2", got)
			}
			if got := *policy.Rules[1].Wait.Seconds; got != 10 {
				t.Errorf("DTO mutation changed domain wait seconds: got %d, want 10", got)
			}
		})
	}
}
