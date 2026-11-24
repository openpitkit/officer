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

package officer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
)

const orderAwaitingReviewAlert = "order_awaiting_review"

type orderAwaitingReviewPayload struct {
	Order    domain.ExternalID `json:"order"`
	Account  domain.AccountID  `json:"account"`
	MarkCode string            `json:"markCode"`
}

func orderAwaitingReviewType(visible func(context.Context, domain.AccountID) (bool, error), orders interface {
	GetOrder(context.Context, domain.ExternalID) (domain.OrderDetail, error)
}) alerts.Type {
	return alerts.Type{
		Name:    orderAwaitingReviewAlert,
		Visible: accountAlertVisibility(visible, "order awaiting review"),
		Validate: func(raw json.RawMessage) error {
			var payload struct {
				Order    *domain.ExternalID `json:"order"`
				Account  *domain.AccountID  `json:"account"`
				MarkCode *string            `json:"markCode"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return fmt.Errorf("order awaiting review payload: %w", err)
			}
			if payload.Order == nil || payload.Account == nil || payload.MarkCode == nil || *payload.MarkCode == "" {
				return fmt.Errorf("order awaiting review requires order, account and markCode")
			}
			if err := domain.ValidateExternalID(payload.Order.String()); err != nil {
				return domain.Internal(err)
			}
			return domain.Internal(domain.ValidateAccountID(*payload.Account))
		},
		Actions: map[string]alerts.Action{
			"close": {
				Validate: validateEmptyAlertInput,
				Resolve: func(ctx context.Context, event alerts.Event, _ json.RawMessage) (json.RawMessage, error) {
					var payload orderAwaitingReviewPayload
					if err := json.Unmarshal(event.Payload, &payload); err != nil {
						return nil, err
					}
					order, err := orders.GetOrder(ctx, payload.Order)
					// A missing order (deleted with its account or instrument)
					// is the "order no longer exists" outcome, not an error.
					if err != nil && !errors.Is(err, domain.ErrNotFound) {
						return nil, err
					}
					if err == nil {
						if err := domain.RequireOrderNotAwaitingReview(order.Order.Status); err != nil {
							return nil, err
						}
					}
					return json.RawMessage(`{"closed":true}`), nil
				},
			},
		},
	}
}

type orderReviewAlertProducer struct {
	service *alerts.Service
}

var _ node.OrderReviewObserver = orderReviewAlertProducer{}

func (p orderReviewAlertProducer) OrderAwaitingReview(ctx context.Context, order domain.Order, mark domain.ReviewMark, caller domain.Caller) error {
	payload, err := json.Marshal(orderAwaitingReviewPayload{Order: order.ExternalID, Account: order.Account, MarkCode: mark.Code})
	if err != nil {
		return err
	}
	event := alerts.Event{ID: order.ExternalID.String(), Type: orderAwaitingReviewAlert, At: time.Now().UTC(), Source: caller.Source, Payload: payload}
	return p.service.Publish(auth.ContextWithCaller(ctx, caller), event)
}

func (p orderReviewAlertProducer) OrderReviewDecided(ctx context.Context, order domain.Order, caller domain.Caller) error {
	_, err := p.service.Resolve(auth.ContextWithCaller(ctx, caller), order.ExternalID.String(), "close", json.RawMessage(`{}`))
	return err
}
