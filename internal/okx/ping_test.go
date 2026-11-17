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

package okx_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.openpit.dev/officer/internal/okx"
)

type writerFunc func(context.Context, []byte) error

func (f writerFunc) Write(ctx context.Context, payload []byte) error { return f(ctx, payload) }

func TestPingLoopTicksAndCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := make(chan string, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		okx.PingLoop(ctx, writerFunc(func(_ context.Context, p []byte) error {
			select {
			case writes <- string(p):
			case <-ctx.Done():
			}
			return nil
		}), time.Millisecond, make(chan error, 1))
	}()
	for range 2 {
		select {
		case got := <-writes:
			if got != "ping" {
				t.Fatalf("payload = %q; want ping", got)
			}
		case <-time.After(time.Second):
			t.Fatal("no ping on tick")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop after cancellation")
	}
}

func TestPingLoopDisabled(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		t.Run(interval.String(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			calls := 0
			go func() {
				defer close(done)
				okx.PingLoop(ctx, writerFunc(func(context.Context, []byte) error { calls++; return nil }), interval, nil)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("disabled loop did not return")
			}
			if calls != 0 {
				t.Fatalf("writes = %d; want 0", calls)
			}
		})
	}
}

func TestPingLoopWriteError(t *testing.T) {
	for _, test := range []struct {
		name     string
		errs     chan error
		reported bool
	}{
		{"available", make(chan error, 1), true},
		{"unread", make(chan error), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := errors.New("write failed")
			done := make(chan struct{})
			calls := 0
			go func() {
				defer close(done)
				okx.PingLoop(ctx, writerFunc(func(context.Context, []byte) error { calls++; return want }), time.Millisecond, test.errs)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("loop did not stop on write error")
			}
			if calls != 1 {
				t.Fatalf("writes = %d; want 1", calls)
			}
			if test.reported {
				select {
				case got := <-test.errs:
					if got != want {
						t.Fatalf("error = %v; want original write error", got)
					}
				default:
					t.Fatal("write error not reported")
				}
			}
		})
	}
}
