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
	"strings"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

const plainDecimalMaxLength = 40

// parsePlainDecimal reads a non-negative plain decimal. It refuses a negative
// value, a leading "+", an exponent, whitespace, NaN and values longer than
// plainDecimalMaxLength, and zero when positive is set.
func parsePlainDecimal(raw string, positive bool) (decimal.Decimal, error) {
	if len(raw) > plainDecimalMaxLength {
		return decimal.Decimal{}, errors.New("decimal exceeds length limit")
	}
	if strings.HasPrefix(raw, "+") {
		return decimal.Decimal{}, errors.New("invalid plain decimal")
	}
	value, err := domain.ParseOpenQuantity(raw)
	if err != nil || (positive && !value.IsPositive()) {
		return decimal.Decimal{}, errors.New("invalid plain decimal")
	}
	return value, nil
}

// spotCommission reads a venue fee as a non-negative plain decimal. A non-zero
// fee needs its asset; a zero fee is no commission (nil).
func spotCommission(amount, asset string) (*domain.Commission, error) {
	value, err := parsePlainDecimal(amount, false)
	if err != nil || (asset == "" && !value.IsZero()) {
		return nil, errors.New("unreadable commission")
	}
	if value.IsZero() {
		return nil, nil
	}
	return &domain.Commission{Amount: amount, Currency: asset}, nil
}

// commissionLeg matches a fee asset to base or quote; others have no leg.
func commissionLeg(asset, base, quote string) fwtrading.CommissionLeg {
	var leg fwtrading.CommissionLeg
	switch asset {
	case base:
		leg = fwtrading.CommissionLegBase
	case quote:
		leg = fwtrading.CommissionLegQuote
	}
	return leg
}
