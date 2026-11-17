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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
	"go.openpit.dev/officer/framework/domain"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

const venueDecimalMaxLength = 40

func parseVenueRoute(raw string, limit bool, vendor string) (string, error) {
	var route map[string]string
	if json.Unmarshal([]byte(raw), &route) != nil || route == nil {
		return "", fmt.Errorf("%w: invalid %s route JSON",
			fwtrading.ErrRejected, vendor)
	}
	for field := range route {
		if field != "timeInForce" {
			return "", fmt.Errorf("%w: unknown %s route field",
				fwtrading.ErrRejected, vendor)
		}
	}
	tif, present := route["timeInForce"]
	if !limit {
		if present {
			return "", fmt.Errorf("%w: market route must be empty",
				fwtrading.ErrRejected)
		}
		return "", nil
	}
	switch tif {
	case "GTC", "IOC", "FOK":
		return tif, nil
	default:
		return "", fmt.Errorf("%w: limit timeInForce must be GTC, IOC or FOK",
			fwtrading.ErrRejected)
	}
}

func parseVenueDecimal(raw string, positive bool) (decimal.Decimal, error) {
	if len(raw) > venueDecimalMaxLength {
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

func isDecimalID(id string) bool {
	if id == "" {
		return false
	}
	for _, char := range id {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
