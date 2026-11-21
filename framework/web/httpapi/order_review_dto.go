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

	"go.openpit.dev/officer/framework/backend"
	"go.openpit.dev/officer/framework/domain"
)

// ReviewMarkDTO is a policy review mark shared by REST and MCP.
type ReviewMarkDTO struct {
	OrderRejectDTO
	Mode string `json:"mode"`
}

// ToReviewMarkDTOs preserves mark order and emits an empty array for no marks.
func ToReviewMarkDTOs(marks []domain.ReviewMark) []ReviewMarkDTO {
	out := make([]ReviewMarkDTO, 0, len(marks))
	for _, mark := range marks {
		out = append(out, ReviewMarkDTO{
			OrderRejectDTO: ToOrderRejectDTO(mark.OrderReject),
			Mode:           string(mark.Mode),
		})
	}
	return out
}

// OrderReviewDTO shares the review wire shape while each surface owns its route
// encoding.
type OrderReviewDTO[RouteDTO any] struct {
	Held        bool                            `json:"held"`
	Marks       []ReviewMarkDTO                 `json:"marks"`
	LastConfirm *ReviewConfirmDTO               `json:"lastConfirm"`
	Destination *ReviewDestinationDTO[RouteDTO] `json:"destination"`
}

// ReviewDestinationDTO carries a route in its surface's existing
// representation.
type ReviewDestinationDTO[RouteDTO any] struct {
	Connection   string   `json:"connection"`
	VenueAccount string   `json:"venueAccount"`
	Route        RouteDTO `json:"route"`
}

// ReviewConfirmDTO is the last recorded confirmation attempt shared by REST
// and MCP.
type ReviewConfirmDTO struct {
	Verdict string           `json:"verdict"`
	Reasons []OrderRejectDTO `json:"reasons"`
	At      string           `json:"at"`
}

// ToOrderReviewDTO maps a review using the surface's stored-route converter.
func ToOrderReviewDTO[RouteDTO any](view *backend.ReviewView, route func(string) (RouteDTO, error)) (OrderReviewDTO[RouteDTO], error) {
	out := OrderReviewDTO[RouteDTO]{Held: view.Held, Marks: ToReviewMarkDTOs(view.Marks)}
	if view.LastConfirm != nil {
		out.LastConfirm = &ReviewConfirmDTO{
			Verdict: string(view.LastConfirm.Verdict),
			Reasons: ToOrderRejectDTOs(view.LastConfirm.Reasons),
			At:      view.LastConfirm.At.Format(time.RFC3339Nano),
		}
	}
	if dest := view.Destination; dest != nil {
		encoded, err := route(dest.Route)
		if err != nil {
			return OrderReviewDTO[RouteDTO]{}, err
		}
		out.Destination = &ReviewDestinationDTO[RouteDTO]{
			Connection: dest.Connection.String(), VenueAccount: dest.VenueAccount,
			Route: encoded,
		}
	}
	return out, nil
}
