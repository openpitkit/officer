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
	"fmt"
	"strings"

	fwtrading "go.openpit.dev/officer/framework/trading"
)

// parseSpotRoute reads a spot route: a limit order's timeInForce, which must
// be GTC, IOC or FOK, or "" for a market order, whose route must be an empty
// object. It refuses JSON that is not an object, null and any other field;
// venue only names the venue in the JSON and unknown-field messages.
func parseSpotRoute(raw, venue string, limit bool) (string, error) {
	var route map[string]string
	if json.Unmarshal([]byte(raw), &route) != nil || route == nil {
		return "", fmt.Errorf("%w: invalid %s route JSON",
			fwtrading.ErrRejected, venue)
	}
	for field := range route {
		if field != "timeInForce" {
			return "", fmt.Errorf("%w: unknown %s route field",
				fwtrading.ErrRejected, venue)
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

// parseTimeInForceRoute validates a venue's timeInForce route.
func parseTimeInForceRoute(raw, venue string, allowed ...string) (string, error) {
	var route map[string]string
	if err := json.Unmarshal([]byte(raw), &route); err != nil {
		return "", fmt.Errorf("%w: invalid %s route JSON", fwtrading.ErrRejected, venue)
	}
	for field := range route {
		if field != "timeInForce" {
			return "", fmt.Errorf("%w: unknown %s route field", fwtrading.ErrRejected, venue)
		}
	}
	tif := route["timeInForce"]
	if tif == "" {
		return "", fmt.Errorf("%w: timeInForce is required", fwtrading.ErrRejected)
	}
	for _, value := range allowed {
		if tif == value {
			return tif, nil
		}
	}
	return "", fmt.Errorf("%w: %s timeInForce must be one of %s (case-sensitive)",
		fwtrading.ErrRejected, venue, strings.Join(allowed, ", "))
}
