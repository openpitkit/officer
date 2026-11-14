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

package domain

import "strings"

// InstrumentMask selects base and quote asset codes separately by case-sensitive,
// byte-exact full matches. Only * is special: it matches any run, including empty.
// Every other character is literal. The zero value matches nothing.
type InstrumentMask struct {
	baseAsset, quoteAsset string
}

// ParseInstrumentMask validates the base mask before the quote mask with
// ValidateAsset, preserving the entered text. Refusals carry ErrInvalid, the
// /baseAsset or /quoteAsset pointer, and the required (empty) or format constraint.
func ParseInstrumentMask(baseAsset, quoteAsset string) (InstrumentMask, error) {
	if err := ValidateAsset(baseAsset); err != nil {
		constraint := "format"
		if baseAsset == "" {
			constraint = "required"
		}
		return InstrumentMask{}, NewValidationError(
			"/baseAsset", constraint, "base asset mask: "+err.Error(),
		)
	}
	if err := ValidateAsset(quoteAsset); err != nil {
		constraint := "format"
		if quoteAsset == "" {
			constraint = "required"
		}
		return InstrumentMask{}, NewValidationError(
			"/quoteAsset", constraint, "quote asset mask: "+err.Error(),
		)
	}
	return InstrumentMask{baseAsset: baseAsset, quoteAsset: quoteAsset}, nil
}

// BaseAsset returns the base mask exactly as entered, without normalization.
func (m InstrumentMask) BaseAsset() string { return m.baseAsset }

// QuoteAsset returns the quote mask exactly as entered, without normalization.
func (m InstrumentMask) QuoteAsset() string { return m.quoteAsset }

// Matches requires separate full matches of both codes, case-sensitively and
// byte-exactly, with only * matching any run, including empty, / and *.
// It does not validate candidate codes. The zero value always returns false.
func (m InstrumentMask) Matches(baseAsset, quoteAsset string) bool {
	if m.baseAsset == "" || m.quoteAsset == "" {
		return false
	}
	return matchesAssetMask(m.baseAsset, baseAsset) &&
		matchesAssetMask(m.quoteAsset, quoteAsset)
}

func matchesAssetMask(mask, code string) bool {
	fragments := strings.Split(mask, "*")
	if len(fragments) == 1 {
		return mask == code
	}
	prefix, suffix := fragments[0], fragments[len(fragments)-1]
	if len(code) < len(prefix)+len(suffix) {
		return false
	}
	if !strings.HasPrefix(code, prefix) || !strings.HasSuffix(code, suffix) {
		return false
	}
	start, end := len(prefix), len(code)-len(suffix)
	for _, fragment := range fragments[1 : len(fragments)-1] {
		index := strings.Index(code[start:end], fragment)
		if index < 0 {
			return false
		}
		start += index + len(fragment)
	}
	return true
}
