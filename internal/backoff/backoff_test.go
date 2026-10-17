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

package backoff

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDelayCaps(t *testing.T) {
	t.Parallel()

	if got := Delay(1, time.Second, 8*time.Second); got != time.Second {
		t.Fatalf("Delay(1) = %s", got)
	}
	if got := Delay(4, time.Second, 8*time.Second); got != 8*time.Second {
		t.Fatalf("Delay(4) = %s", got)
	}
	if got := Delay(10, time.Second, 8*time.Second); got != 8*time.Second {
		t.Fatalf("Delay(10) = %s", got)
	}
}

func TestSleepEndsWithContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep(canceled) = %v", err)
	}
}

func TestSleepWaitsForDelay(t *testing.T) {
	t.Parallel()

	const delay = 10 * time.Millisecond
	start := time.Now()
	if err := Sleep(t.Context(), delay); err != nil {
		t.Fatalf("Sleep = %v", err)
	}
	if waited := time.Since(start); waited < delay {
		t.Fatalf("Sleep returned after %s, want at least %s", waited, delay)
	}
}
