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

import "errors"

// ErrTradingAccessNotification means access was committed but its warning could
// not be inspected, published or delivered. Retrying the mutation is not recovery.
var ErrTradingAccessNotification = registerSentinel(errors.New("access change was saved, but its mixed paper/live warning could not be published or delivered; review current access and alerts before retrying"))

// TradingAccessNotificationError isolates a warning failure from independent
// operational failures joined after an already committed administrative change.
type TradingAccessNotificationError struct {
	Cause error
}

func (e *TradingAccessNotificationError) Error() string {
	return ErrTradingAccessNotification.Error() + ": " + e.Cause.Error()
}

func (e *TradingAccessNotificationError) Unwrap() error { return e.Cause }

func (e *TradingAccessNotificationError) Is(target error) bool {
	return target == ErrTradingAccessNotification
}

// OnlyTradingAccessNotification reports whether every joined failure is a
// warning failure. Operational completion errors must retain their own guidance.
func OnlyTradingAccessNotification(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := err.(*TradingAccessNotificationError); ok || err == ErrTradingAccessNotification {
		return true
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !OnlyTradingAccessNotification(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return OnlyTradingAccessNotification(wrapped.Unwrap())
	}
	return false
}
