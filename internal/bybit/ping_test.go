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
	"errors"
	"testing"
	"time"
)

func TestPingLoopTick(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const interval = 30 * time.Millisecond
	payloads := make(chan string, 1)
	done := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		PingLoop(ctx, func(writeCtx context.Context, payload []byte) error {
			if writeCtx != ctx {
				t.Error("write context differs from loop context")
			}
			select {
			case payloads <- string(payload):
			default:
			}
			return nil
		}, interval, nil)
	}()
	payload := <-payloads
	if payload != `{"op":"ping"}` {
		t.Errorf("payload = %q, want %q", payload, `{"op":"ping"}`)
	}
	if elapsed := time.Since(start); elapsed < interval {
		t.Errorf("ping after %s, want at least %s", elapsed, interval)
	}
	cancel()
	<-done
}

func TestPingLoopCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	writes := make(chan struct{}, 1)
	go func() {
		defer close(done)
		PingLoop(ctx, func(context.Context, []byte) error {
			writes <- struct{}{}
			return nil
		}, time.Hour, nil)
	}()
	cancel()
	<-done
	select {
	case <-writes:
		t.Fatal("ping written before first tick")
	default:
	}
}

func TestPingLoopWriteError(t *testing.T) {
	for _, capacity := range []int{1, 0} {
		name := "buffered"
		if capacity == 0 {
			name = "unread"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := errors.New("write failed")
			errs := make(chan error, capacity)
			done := make(chan struct{})
			calls := 0
			go func() {
				defer close(done)
				PingLoop(ctx, func(context.Context, []byte) error {
					calls++
					return want
				}, time.Millisecond, errs)
			}()
			<-done
			if calls != 1 {
				t.Errorf("writes = %d, want 1", calls)
			}
			if capacity > 0 {
				select {
				case got := <-errs:
					if !errors.Is(got, want) {
						t.Errorf("error = %v, want %v", got, want)
					}
				default:
					t.Fatal("write error was not reported")
				}
			}
		})
	}
}

func TestPingLoopDisabled(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			errs := make(chan error, 1)
			PingLoop(context.Background(), func(context.Context, []byte) error {
				t.Error("disabled PingLoop wrote a ping")
				return errors.New("unexpected write")
			}, interval, errs)
			select {
			case err := <-errs:
				t.Fatalf("disabled PingLoop reported error: %v", err)
			default:
			}
		})
	}
}
