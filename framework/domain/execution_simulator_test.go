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

package domain_test

import (
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestProviderSupportsExecutionPolicy(t *testing.T) {
	for _, supported := range []bool{true, false} {
		got, err := domain.ProviderSupportsExecutionPolicy(func(provider string) (bool, error) {
			if provider != "fixture" {
				t.Fatalf("provider = %q", provider)
			}
			return supported, nil
		}, "fixture")
		if err != nil || got != supported {
			t.Fatalf("capability = %v, %v", got, err)
		}
	}
	upstream := errors.New("lookup failed")
	if _, err := domain.ProviderSupportsExecutionPolicy(func(string) (bool, error) { return false, upstream }, "fixture"); !errors.Is(err, upstream) {
		t.Fatal(err)
	}
	if _, err := domain.ProviderSupportsExecutionPolicy(nil, "fixture"); !errors.Is(err, domain.ErrTradingProviderUnavailable) {
		t.Fatal(err)
	}
}

func TestValidateTradingIsolation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		modes []domain.TradingMode
		want  error
	}{
		{"empty", nil, nil},
		{"paper", []domain.TradingMode{domain.TradingModePaper, domain.TradingModePaper}, nil},
		{"live", []domain.TradingMode{domain.TradingModeLive, domain.TradingModeLive}, nil},
		{"mixed", []domain.TradingMode{domain.TradingModePaper, domain.TradingModeLive}, nil},
		{"reverse", []domain.TradingMode{domain.TradingModeLive, domain.TradingModePaper}, nil},
		{"unknown", []domain.TradingMode{"unknown"}, domain.ErrInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.ValidateTradingAccessModes("account", tc.modes); !errors.Is(err, tc.want) {
				t.Fatalf("isolation = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestValidateTestExecutionPolicy(t *testing.T) {
	mask, err := domain.ParseInstrumentMask("A*", "USD")
	if err != nil {
		t.Fatal(err)
	}
	fills2, fills3, fills1, fills4 := 2, 3, 1, 4
	zero, one, maximum, overflow := int64(0), int64(1), int64(9223372036), int64(9223372037)
	finite := &domain.TestExecutionWait{Kind: "finite", Seconds: &one}
	infinite := &domain.TestExecutionWait{Kind: "infinite"}
	cases := []struct {
		name                string
		mode                domain.TestExecutionMode
		variant             domain.TestPriceVariant
		fills               *int
		wait                *domain.TestExecutionWait
		pointer, constraint string
	}{
		{"execute", "execute", "normal", nil, nil, "", ""},
		{"best", "execute", "best", nil, nil, "", ""},
		{"worst", "execute", "worst", nil, nil, "", ""},
		{"reject", "reject_even", "normal", nil, nil, "", ""},
		{"split two", "split", "normal", &fills2, nil, "", ""},
		{"split three", "split", "normal", &fills3, nil, "", ""},
		{"finite one", "rest", "normal", nil, finite, "", ""},
		{"finite maximum", "rest", "normal", nil, &domain.TestExecutionWait{Kind: "finite", Seconds: &maximum}, "", ""},
		{"infinite", "rest", "", nil, infinite, "", ""},
		{"mode required", "", "normal", nil, nil, "mode", "required"},
		{"mode enum", "other", "normal", nil, nil, "mode", "enum"},
		{"execute variant required", "execute", "", nil, nil, "priceVariant", "required"},
		{"reject variant required", "reject_even", "", nil, nil, "priceVariant", "required"},
		{"split variant required", "split", "", &fills2, nil, "priceVariant", "required"},
		{"finite variant required", "rest", "", nil, finite, "priceVariant", "required"},
		{"execute variant enum", "execute", "other", nil, nil, "priceVariant", "enum"},
		{"reject variant enum", "reject_even", "other", nil, nil, "priceVariant", "enum"},
		{"split variant enum", "split", "other", &fills2, nil, "priceVariant", "enum"},
		{"finite variant enum", "rest", "other", nil, finite, "priceVariant", "enum"},
		{"execute fills", "execute", "normal", &fills2, nil, "fills", "forbidden"},
		{"reject fills", "reject_even", "normal", &fills2, nil, "fills", "forbidden"},
		{"rest fills", "rest", "normal", &fills2, finite, "fills", "forbidden"},
		{"execute wait", "execute", "normal", nil, finite, "wait", "forbidden"},
		{"reject wait", "reject_even", "normal", nil, finite, "wait", "forbidden"},
		{"split wait", "split", "normal", &fills2, finite, "wait", "forbidden"},
		{"split fills required", "split", "normal", nil, nil, "fills", "required"},
		{"split fills low", "split", "normal", &fills1, nil, "fills", "enum"},
		{"split fills high", "split", "normal", &fills4, nil, "fills", "enum"},
		{"rest wait required", "rest", "normal", nil, nil, "wait", "required"},
		{"wait kind required", "rest", "normal", nil, &domain.TestExecutionWait{}, "wait/kind", "required"},
		{"wait kind enum", "rest", "normal", nil, &domain.TestExecutionWait{Kind: "other"}, "wait/kind", "enum"},
		{"seconds required", "rest", "normal", nil, &domain.TestExecutionWait{Kind: "finite"}, "wait/seconds", "required"},
		{"seconds low", "rest", "normal", nil, &domain.TestExecutionWait{Kind: "finite", Seconds: &zero}, "wait/seconds", "range"},
		{"seconds high", "rest", "normal", nil, &domain.TestExecutionWait{Kind: "finite", Seconds: &overflow}, "wait/seconds", "range"},
		{"infinite seconds", "rest", "", nil, &domain.TestExecutionWait{Kind: "infinite", Seconds: &one}, "wait/seconds", "forbidden"},
		{"infinite variant", "rest", "normal", nil, infinite, "priceVariant", "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{{Mask: mask, Mode: tc.mode, PriceVariant: tc.variant, Fills: tc.fills, Wait: tc.wait}}}
			err := domain.ValidateTestExecutionPolicy(policy)
			if tc.constraint == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, domain.ErrInvalid) || domain.ValidationPointer(err) != "/rules/0/"+tc.pointer || domain.ValidationConstraint(err) != tc.constraint {
				t.Fatalf("error=%v pointer=%q constraint=%q", err, domain.ValidationPointer(err), domain.ValidationConstraint(err))
			}
		})
	}
	if err := domain.ValidateTestExecutionPolicy(domain.TestExecutionPolicy{}); domain.ValidationPointer(err) != "/rules" || domain.ValidationConstraint(err) != "required" {
		t.Fatalf("nil rules: %v", err)
	}
	if err := domain.ValidateTestExecutionPolicy(domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{}}); err != nil {
		t.Fatal(err)
	}
	rule := domain.TestExecutionRule{Mask: mask, Mode: "execute", PriceVariant: "normal"}
	if err := domain.ValidateTestExecutionPolicy(domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{rule, rule}}); err != nil {
		t.Fatalf("duplicates: %v", err)
	}
	rule.Mask = domain.InstrumentMask{}
	if err := domain.ValidateTestExecutionPolicy(domain.TestExecutionPolicy{Rules: []domain.TestExecutionRule{{Mask: mask, Mode: "execute", PriceVariant: "normal"}, rule}}); domain.ValidationPointer(err) != "/rules/1/mask/baseAsset" || domain.ValidationConstraint(err) != "required" {
		t.Fatalf("zero mask: %v", err)
	}
}
