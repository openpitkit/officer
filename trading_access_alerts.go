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
	"fmt"
	"time"

	"go.openpit.dev/officer/framework/alerts"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

const mixedTradingAccessAlert = "mixed_trading_access"

type mixedTradingAccessPayload struct {
	Account domain.AccountID `json:"account"`
	Title   string           `json:"title"`
}

func mixedTradingAccessType(visible func(context.Context, domain.AccountID) (bool, error)) alerts.Type {
	return alerts.Type{
		Name:    mixedTradingAccessAlert,
		Visible: accountAlertVisibility(visible, "mixed trading access"),
		Validate: func(raw json.RawMessage) error {
			var payload struct {
				Account *domain.AccountID `json:"account"`
				Title   *string           `json:"title"`
			}
			if err := json.Unmarshal(raw, &payload); err != nil {
				return fmt.Errorf("mixed trading access payload: %w: %w", domain.ErrInvalid, err)
			}
			if payload.Account == nil || payload.Title == nil {
				return fmt.Errorf("mixed trading access requires account and title: %w", domain.ErrInvalid)
			}
			return domain.ValidateAccountID(*payload.Account)
		},
		Actions: map[string]alerts.Action{
			"acknowledge": {
				Validate: validateEmptyAlertInput,
				Resolve: func(context.Context, alerts.Event, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{"acknowledged":true}`), nil
				},
			},
		},
	}
}

func accountAlertVisibility(visible func(context.Context, domain.AccountID) (bool, error), name string) func(context.Context, alerts.Event) (bool, error) {
	return func(ctx context.Context, event alerts.Event) (bool, error) {
		if visible == nil {
			return false, fmt.Errorf("%s account visibility is not bound", name)
		}
		var payload struct {
			Account domain.AccountID `json:"account"`
		}
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return false, err
		}
		return visible(ctx, payload.Account)
	}
}

func validateEmptyAlertInput(raw json.RawMessage) error {
	var input map[string]json.RawMessage
	if err := json.Unmarshal(raw, &input); err != nil || input == nil || len(input) != 0 {
		return fmt.Errorf("alert action requires an empty object: %w", domain.ErrInvalid)
	}
	return nil
}

func publishMixedTradingAccess(service *alerts.Service) func(context.Context, domain.Account, domain.Caller) error {
	return func(ctx context.Context, account domain.Account, caller domain.Caller) error {
		id, err := domain.NewExternalID()
		if err != nil {
			return err
		}
		payload, err := json.Marshal(mixedTradingAccessPayload{Account: account.Code, Title: account.Title})
		if err != nil {
			return err
		}
		// One event identity belongs to this publication. Service.Publish stores it
		// before delivery and reports delivery failures without changing access.
		event := alerts.Event{ID: id.String(), Type: mixedTradingAccessAlert, At: time.Now().UTC(), Source: caller.Source, Payload: payload}
		return service.Publish(auth.ContextWithCaller(ctx, caller), event)
	}
}
