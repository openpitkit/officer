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

package bybit

import (
	"context"
	"time"
)

// PingLoop writes a ping each interval until cancellation or a write error.
// A non-positive interval disables pings. Write errors are sent to errs
// without blocking; the caller owns the channel.
func PingLoop(
	ctx context.Context,
	write func(context.Context, []byte) error,
	interval time.Duration,
	errs chan<- error,
) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := write(ctx, []byte(`{"op":"ping"}`)); err != nil {
				select {
				case errs <- err:
				default:
				}
				return
			}
		}
	}
}
