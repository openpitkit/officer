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

// Package backoff holds the one reconnect-delay formula and the one
// cancellable wait that market-data and trading stream connectors share; each
// connector's run loop keeps its own policy for when to grow or reset the
// attempt count.
package backoff

import (
	"context"
	"time"
)

// Delay returns the wait before reconnect attempt attempt: minDelay when
// attempt <= 0, otherwise minDelay doubled attempt-1 times, capped at
// maxDelay.
func Delay(attempt int, minDelay, maxDelay time.Duration) time.Duration {
	if attempt <= 0 {
		return minDelay
	}
	delay := minDelay
	for range attempt - 1 {
		if delay >= maxDelay/2 {
			return maxDelay
		}
		delay *= 2
	}
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

// Sleep waits for delay unless ctx ends first: it returns ctx.Err() when ctx
// ends the wait and nil when delay elapses.
func Sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
