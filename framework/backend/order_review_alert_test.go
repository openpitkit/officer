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

package backend

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestFinishOrderReviewNotifications(t *testing.T) {
	operationErr := errors.New("operation failed")
	cause := errors.New("alert unavailable")
	alertErr := &domain.OrderReviewAlertError{Step: domain.OrderReviewAlertStepPublish, Cause: cause}
	otherErr := errors.New("another notification failed")

	t.Run("operation error identity", func(t *testing.T) {
		failure, err := finishOrderReviewNotifications(operationErr, nil)
		if failure != nil || err != operationErr {
			t.Fatalf("finish = %+v, %v; want original operation error", failure, err)
		}
	})
	t.Run("operation and alert errors", func(t *testing.T) {
		notificationErr := errors.Join(alertErr)
		failure, err := finishOrderReviewNotifications(operationErr, notificationErr)
		var got *domain.OrderReviewAlertError
		if failure != nil || !errors.Is(err, operationErr) || !errors.Is(err, notificationErr) ||
			!errors.Is(err, cause) || !errors.As(err, &got) || got != alertErr {
			t.Fatalf("finish = %+v, %v; want both errors preserved", failure, err)
		}
	})
	t.Run("one alert", func(t *testing.T) {
		for _, notificationErr := range []error{alertErr, errors.Join(alertErr)} {
			failure, err := finishOrderReviewNotifications(nil, notificationErr)
			if err != nil || failure == nil || failure.Step != alertErr.Step || failure.Message != cause.Error() {
				t.Fatalf("finish = %+v, %v; want explicit alert failure", failure, err)
			}
		}
	})
	t.Run("two alerts", func(t *testing.T) {
		second := &domain.OrderReviewAlertError{Step: domain.OrderReviewAlertStepClose, Cause: errors.New("close failed")}
		failure, err := finishOrderReviewNotifications(nil, errors.Join(alertErr, second))
		if failure != nil || err == nil || !errors.Is(err, alertErr) || !errors.Is(err, second) ||
			!strings.Contains(err.Error(), "multiple order review alert failures") {
			t.Fatalf("finish = %+v, %v; want invariant error with both alerts", failure, err)
		}
	})
	t.Run("alert and another error", func(t *testing.T) {
		failure, err := finishOrderReviewNotifications(nil, errors.Join(alertErr, otherErr))
		if err != otherErr || failure == nil || failure.Step != alertErr.Step || failure.Message != cause.Error() {
			t.Fatalf("finish = %+v, %v; want alert failure and original other error", failure, err)
		}
	})
	t.Run("alert and two other errors", func(t *testing.T) {
		second := errors.New("second notification failed")
		failure, err := finishOrderReviewNotifications(nil, errors.Join(otherErr, alertErr, second))
		if failure == nil || failure.Step != alertErr.Step || failure.Message != cause.Error() ||
			!errors.Is(err, otherErr) || !errors.Is(err, second) {
			t.Fatalf("finish = %+v, %v; want alert failure and both other errors", failure, err)
		}
	})
	t.Run("another error identity", func(t *testing.T) {
		for _, notificationErr := range []error{otherErr, errors.Join(otherErr)} {
			failure, err := finishOrderReviewNotifications(nil, notificationErr)
			if failure != nil || err != otherErr {
				t.Fatalf("finish = %+v, %v; want original other error", failure, err)
			}
		}
	})
	t.Run("wrapped alert stays another error", func(t *testing.T) {
		wrapped := fmt.Errorf("another callback: %w", alertErr)
		failure, err := finishOrderReviewNotifications(nil, errors.Join(wrapped))
		if failure != nil || err != wrapped {
			t.Fatalf("finish = %+v, %v; want original wrapper", failure, err)
		}
	})
	t.Run("nested join stays another error", func(t *testing.T) {
		nested := errors.Join(alertErr, otherErr)
		failure, err := finishOrderReviewNotifications(nil, errors.Join(nested))
		if failure != nil || err != nested {
			t.Fatalf("finish = %+v, %v; want original nested join", failure, err)
		}
	})
}
