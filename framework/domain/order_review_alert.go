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

// Order review alert steps identify the notification after a committed change.
const (
	OrderReviewAlertStepPublish = "publish"
	OrderReviewAlertStepClose   = "close"
)

// OrderReviewAlertError reports an alert failure after an order was committed.
// Retrying the order operation is not recovery.
type OrderReviewAlertError struct {
	Step  string
	Cause error
}

func (e *OrderReviewAlertError) Error() string {
	return "order review alert " + e.Step + ": " + e.Cause.Error()
}

func (e *OrderReviewAlertError) Unwrap() error {
	return e.Cause
}
