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

package scheduler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.openpit.dev/officer/framework/scheduler"
)

type blockingClaimStore struct {
	scheduler.Store
	entered  chan struct{}
	claimErr func(context.Context) error
}

func (s *blockingClaimStore) Recover(context.Context, time.Time) error { return nil }

func (s *blockingClaimStore) Claim(ctx context.Context, _ time.Time) (scheduler.Job, bool, error) {
	close(s.entered)
	<-ctx.Done()
	return scheduler.Job{}, false, s.claimErr(ctx)
}

func TestStopDuringClaim(t *testing.T) {
	genuineErr := errors.New("store failed")
	for _, test := range []struct {
		name     string
		claimErr func(context.Context) error
		want     error
	}{
		{
			name: "wrapped_cancellation",
			claimErr: func(ctx context.Context) error {
				return fmt.Errorf("store: transaction ended by its context: %w: %w", ctx.Err(), errors.New("interrupted (9)"))
			},
		},
		{
			name:     "genuine_error",
			claimErr: func(context.Context) error { return genuineErr },
			want:     genuineErr,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &blockingClaimStore{
				entered: make(chan struct{}), claimErr: test.claimErr,
			}
			worker, err := scheduler.New(store, func(string, json.RawMessage) (scheduler.Task, error) {
				return nil, errors.New("unexpected task factory call")
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := worker.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() {
				if err := worker.Stop(); err != nil {
					t.Errorf("cleanup Stop: %v", err)
				}
			})
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-store.entered:
			case <-timer.C:
				t.Fatal("scheduler did not enter Claim")
			}
			if err := worker.Stop(); !errors.Is(err, test.want) {
				t.Fatalf("Stop: got %v, want %v", err, test.want)
			}
		})
	}
}
