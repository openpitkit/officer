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

package httpapi

import (
	"time"

	"go.openpit.dev/officer/framework/domain"
)

// VenueOrderDTO is the venue order wire shape shared by REST and MCP.
// RouteDTO is the route representation used by the calling surface.
type VenueOrderDTO[RouteDTO any] struct {
	Connection    string   `json:"connection"`
	VenueAccount  string   `json:"venueAccount"`
	Route         RouteDTO `json:"route"`
	ClientOrderID string   `json:"clientOrderId"`
	VenueOrderID  string   `json:"venueOrderId,omitempty"`
	SendAttempted bool     `json:"sendAttempted"`
	CreatedAt     string   `json:"createdAt"`
}

// ToVenueOrderDTO encodes a venue order using the caller's route converter.
func ToVenueOrderDTO[RouteDTO any](v domain.VenueOrder, route func(string) (RouteDTO, error)) (VenueOrderDTO[RouteDTO], error) {
	encoded, err := route(v.Route)
	if err != nil {
		return VenueOrderDTO[RouteDTO]{}, err
	}
	return VenueOrderDTO[RouteDTO]{
		Connection:    v.Connection.String(),
		VenueAccount:  v.VenueAccount,
		Route:         encoded,
		ClientOrderID: v.ClientOrderID,
		VenueOrderID:  v.VenueOrderID,
		SendAttempted: v.SendAttempted,
		CreatedAt:     v.CreatedAt.Format(time.RFC3339Nano),
	}, nil
}
