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

package trading

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"go.openpit.dev/officer/framework/health"
	fwtrading "go.openpit.dev/officer/framework/trading"
)

func subscribeTestLifecycle(t *testing.T, s *streamLifecycle, ctx context.Context,
	stream func(context.Context, chan<- fwtrading.Event) (bool, error),
) <-chan fwtrading.Event {
	t.Helper()
	out, err := s.subscribe(ctx, "test", time.Second, 8*time.Second,
		errors.New("refused"), stream, func(bool, string) {},
		func(health.Diagnostic) {}, func(message string) string { return message })
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStreamLifecycleSubscribe(t *testing.T) {
	for _, state := range []string{"closed", "subscribed", "finished"} {
		t.Run(state, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var s streamLifecycle
				defer s.close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				event := fwtrading.Event{Ref: fwtrading.OrderRef{ClientOrderID: "stream-event"}}
				stream := func(ctx context.Context, out chan<- fwtrading.Event) (bool, error) {
					select {
					case out <- event:
					case <-ctx.Done():
						return false, ctx.Err()
					}
					<-ctx.Done()
					return false, ctx.Err()
				}
				want := "test: stream already subscribed"
				if state == "closed" {
					s.close()
					s.close()
					want = "test: connector is closed"
				} else {
					out := subscribeTestLifecycle(t, &s, ctx, stream)
					if cap(out) != 0 {
						t.Fatal("stream output is buffered")
					}
					synctest.Wait()
					select {
					case got := <-out:
						if got != event {
							t.Fatalf("stream event = %v, want %v", got, event)
						}
					default:
						t.Fatal("stream event was not delivered")
					}
					if state == "finished" {
						cancel()
						synctest.Wait()
						if _, ok := <-out; ok {
							t.Fatal("finished loop left output open")
						}
					}
				}
				out, err := s.subscribe(ctx, "test", time.Second, 8*time.Second,
					errors.New("refused"), stream, func(bool, string) {},
					func(health.Diagnostic) {}, func(message string) string { return message })
				if out != nil || err == nil || err.Error() != want {
					t.Fatalf("Subscribe = (%v, %v), want (nil, %q)", out, err, want)
				}
			})
		})
	}
	for _, missing := range []string{"status", "diagnostic", "both"} {
		t.Run("missing_"+missing, func(t *testing.T) {
			var s streamLifecycle
			defer s.close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var status health.StatusReporter = func(bool, string) {}
			var diagnostic health.DiagnosticReporter = func(health.Diagnostic) {}
			if missing != "diagnostic" {
				status = nil
			}
			if missing != "status" {
				diagnostic = nil
			}
			out, err := s.subscribe(ctx, "test", time.Second, 8*time.Second,
				errors.New("refused"), func(context.Context, chan<- fwtrading.Event) (bool, error) {
					t.Error("stream ran without both reporters")
					return false, errors.New("unexpected session")
				}, status, diagnostic, func(message string) string { return message })
			const want = "test: stream requires a status and a diagnostic reporter"
			if out != nil || err == nil || err.Error() != want {
				t.Fatalf("Subscribe = (%v, %v), want (nil, %q)", out, err, want)
			}
			subscribeTestLifecycle(t, &s, ctx,
				func(ctx context.Context, _ chan<- fwtrading.Event) (bool, error) {
					return false, ctx.Err()
				})
		})
	}
}

func TestStreamLifecycleCloseWaits(t *testing.T) {
	// A blocked mutex prevents synctest.Wait and fake time from advancing.
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	synctest.Test(t, func(t *testing.T) {
		var s streamLifecycle
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan struct{})
		canceled := make(chan struct{})
		release := make(chan struct{})
		returned := make(chan struct{})
		out := subscribeTestLifecycle(t, &s, ctx,
			func(ctx context.Context, _ chan<- fwtrading.Event) (bool, error) {
				defer close(returned)
				close(started)
				<-ctx.Done()
				close(canceled)
				<-release
				return false, ctx.Err()
			})
		<-started
		closed := make(chan struct{})
		go func() {
			s.close()
			close(closed)
		}()
		synctest.Wait()
		select {
		case <-canceled:
		default:
			t.Error("Close did not cancel stream context")
			cancel()
			synctest.Wait()
		}
		select {
		case <-closed:
			t.Error("Close returned before the stream loop returned")
		default:
		}
		select {
		case <-out:
			t.Error("output closed before the stream loop returned")
		default:
		}
		subscribed := make(chan struct{})
		var subscribeOut <-chan fwtrading.Event
		var subscribeErr error
		go func() {
			subscribeOut, subscribeErr = s.subscribe(ctx, "test", time.Second, 8*time.Second,
				errors.New("refused"), func(context.Context, chan<- fwtrading.Event) (bool, error) {
					t.Error("stream ran after Close")
					return false, errors.New("unexpected session")
				}, func(bool, string) {}, func(health.Diagnostic) {},
				func(message string) string { return message })
			close(subscribed)
		}()
		select {
		case <-subscribed:
			synctest.Wait()
			const want = "test: connector is closed"
			if subscribeOut != nil || subscribeErr == nil || subscribeErr.Error() != want {
				t.Errorf("Subscribe = (%v, %v), want (nil, %q)", subscribeOut, subscribeErr, want)
			}
		case <-timeout.C:
			t.Error("Subscribe blocked while Close waited for the stream loop")
		}
		close(release)
		<-closed
		<-subscribed
		select {
		case <-returned:
		default:
			t.Error("stream loop outlived Close")
		}
		select {
		case _, ok := <-out:
			if ok {
				t.Error("unexpected stream event")
			}
		default:
			t.Error("Close returned before output closed")
		}
		s.close()
	})
}

func TestStreamLifecycleReconnect(t *testing.T) {
	refused := errors.New("authorization refused")
	failure := errors.New("disconnected")
	for _, test := range []struct {
		name    string
		errors  []error
		healthy int
		delays  []time.Duration
	}{
		{
			name:   "healthy_resets_attempt",
			errors: []error{failure, failure, failure, failure}, healthy: 3,
			delays: []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, time.Second},
		},
		{
			name:   "refused_until_healthy",
			errors: []error{fmt.Errorf("wrapped: %w", refused), failure, failure, failure}, healthy: 2,
			delays: []time.Duration{8 * time.Second, 8 * time.Second, time.Second, 2 * time.Second},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var s streamLifecycle
				attempts := make(chan time.Time)
				calls := 0
				statuses := 0
				out, err := s.subscribe(context.Background(), "test", time.Second, 8*time.Second,
					refused, func(ctx context.Context, _ chan<- fwtrading.Event) (bool, error) {
						attempts <- time.Now()
						call := calls
						calls++
						if call == len(test.errors) {
							<-ctx.Done()
							return false, ctx.Err()
						}
						return call == test.healthy, test.errors[call]
					}, func(ok bool, message string) {
						if ok || message != test.errors[statuses].Error() {
							t.Errorf("failure status %d = (%v, %q)", statuses, ok, message)
						}
						statuses++
					}, func(health.Diagnostic) {}, func(message string) string { return message })
				if err != nil {
					t.Fatal(err)
				}
				defer s.close()
				previous := <-attempts
				for i, want := range test.delays {
					at := <-attempts
					if got := at.Sub(previous); got != want {
						t.Errorf("retry %d delay = %v, want %v", i+1, got, want)
					}
					previous = at
				}
				s.close()
				if statuses != len(test.errors) {
					t.Errorf("failure statuses = %d, want %d", statuses, len(test.errors))
				}
				if _, ok := <-out; ok {
					t.Fatal("output left open after reconnect loop stopped")
				}
			})
		})
	}
}

func TestStreamLifecycleRedacts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s streamLifecycle
		var statuses []string
		out, err := s.subscribe(context.Background(), "test", time.Second, 8*time.Second,
			errors.New("refused"), func(context.Context, chan<- fwtrading.Event) (bool, error) {
				return false, errors.New("raw-key/raw-secret: disconnected")
			}, func(ok bool, message string) {
				if ok {
					t.Error("failed session reported healthy")
				}
				statuses = append(statuses, message)
			}, func(health.Diagnostic) {},
			strings.NewReplacer("raw-key", "[redacted]", "raw-secret", "[redacted]").Replace)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		s.close()
		if len(statuses) != 1 {
			t.Fatalf("failure statuses = %v, want one redacted failure", statuses)
		}
		if want := "[redacted]/[redacted]: disconnected"; statuses[0] != want {
			t.Fatalf("status message = %q, want %q", statuses[0], want)
		}
		if _, ok := <-out; ok {
			t.Fatal("output left open after Close interrupted backoff")
		}
	})
}

func TestStreamSessionConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		out := make(chan fwtrading.Event)
		reported := false
		var session *streamSession
		var err error
		go func() {
			session, err = connectStream(ctx, out, time.Second,
				func(ok bool, message string) {
					if !ok || message != "" {
						t.Errorf("status = (%v, %q), want (true, empty)", ok, message)
					}
					reported = true
				})
		}()
		synctest.Wait()
		if !reported {
			t.Fatal("connected status not reported before EventConnected delivery")
		}
		time.Sleep(2 * time.Second)
		select {
		case event := <-out:
			if event.Kind != fwtrading.EventConnected {
				t.Fatalf("event = %v, want EventConnected", event)
			}
		default:
			t.Fatal("EventConnected not sent")
		}
		synctest.Wait()
		if err != nil {
			t.Fatal(err)
		}
		if session.healthy() {
			t.Fatal("session clock started before EventConnected delivery")
		}
	})
}

func TestStreamSessionDeliver(t *testing.T) {
	out := make(chan fwtrading.Event, 1)
	session, err := connectStream(context.Background(), out, time.Hour,
		func(bool, string) {})
	if err != nil {
		t.Fatal(err)
	}
	<-out
	event := fwtrading.Event{Kind: "test"}
	if err := session.deliver(event); err != nil {
		t.Fatal(err)
	}
	if got := <-out; got.Kind != event.Kind {
		t.Fatalf("event kind = %q, want %q", got.Kind, event.Kind)
	}
	if !session.healthy() {
		t.Fatal("delivered event did not make session healthy")
	}
}

func TestStreamSessionConnectedNotHealthy(t *testing.T) {
	out := make(chan fwtrading.Event, 2)
	session, err := connectStream(context.Background(), out, time.Hour,
		func(bool, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.deliver(fwtrading.Event{Kind: fwtrading.EventConnected}); err != nil {
		t.Fatal(err)
	}
	if session.healthy() {
		t.Fatal("EventConnected made session healthy")
	}
}

func TestStreamSessionHealthyDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan fwtrading.Event, 1)
		session, err := connectStream(context.Background(), out, time.Second,
			func(bool, string) {})
		if err != nil {
			t.Fatal(err)
		}
		if session.healthy() {
			t.Fatal("new session is healthy")
		}
		time.Sleep(time.Second)
		if session.healthy() {
			t.Fatal("session is healthy at maxDelay")
		}
		time.Sleep(time.Nanosecond)
		if !session.healthy() {
			t.Fatal("session is not healthy after maxDelay")
		}
	})
}

func TestStreamSessionCanceledDelivery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan fwtrading.Event)
	go func() { <-out }()
	session, err := connectStream(ctx, out, time.Hour, func(bool, string) {})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := session.deliver(fwtrading.Event{Kind: "test"}); err != ctx.Err() {
		t.Fatalf("deliver error = %v, want %v", err, ctx.Err())
	}
	if session.healthy() {
		t.Fatal("canceled delivery made session healthy")
	}
	if _, err := connectStream(ctx, out, time.Hour, func(bool, string) {}); err != ctx.Err() {
		t.Fatalf("connect error = %v, want %v", err, ctx.Err())
	}
	select {
	case event := <-out:
		t.Fatalf("canceled delivery sent event: %v", event)
	default:
	}
}

func TestStreamSessionConnectedDoesNotRestartClock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		out := make(chan fwtrading.Event, 2)
		session, err := connectStream(context.Background(), out, time.Second,
			func(bool, string) {})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second + time.Nanosecond)
		if err := session.deliver(fwtrading.Event{Kind: fwtrading.EventConnected}); err != nil {
			t.Fatal(err)
		}
		if !session.healthy() {
			t.Fatal("EventConnected restarted the session clock")
		}
	})
}

func TestStreamLifecycleStartRefusals(t *testing.T) {
	for _, state := range []string{
		"closed", "subscribed", "missing_status", "missing_diagnostic", "missing_both",
	} {
		t.Run(state, func(t *testing.T) {
			var s streamLifecycle
			defer s.close()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			refusals := streamRefusals{
				closed:     "caller closed",
				subscribed: "caller subscribed",
				reporters:  "caller reporters missing",
			}
			stream := func(ctx context.Context, _ chan<- fwtrading.Event) (bool, error) {
				t.Error("stream ran with a canceled context")
				return false, ctx.Err()
			}
			var status health.StatusReporter = func(bool, string) {}
			var diagnostic health.DiagnosticReporter = func(health.Diagnostic) {}
			message := func(text string) string { return text }
			want := refusals.reporters
			switch state {
			case "closed":
				s.close()
				want = refusals.closed
				status, diagnostic = nil, nil
			case "subscribed":
				if _, err := s.start(ctx, refusals, time.Second, 8*time.Second,
					nil, stream, status, diagnostic, message); err != nil {
					t.Fatal(err)
				}
				want = refusals.subscribed
				status, diagnostic = nil, nil
			case "missing_status":
				status = nil
			case "missing_diagnostic":
				diagnostic = nil
			case "missing_both":
				status, diagnostic = nil, nil
			}
			out, err := s.start(ctx, refusals, time.Second, 8*time.Second,
				nil, stream, status, diagnostic, message)
			if out != nil || err == nil || err.Error() != want {
				t.Fatalf("start = (%v, %v), want (nil, %q)", out, err, want)
			}
		})
	}
}

func TestStreamLifecycleStartNilRefusedAuth(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s streamLifecycle
		attempts := make(chan time.Time)
		out, err := s.start(context.Background(), streamRefusals{},
			time.Second, 8*time.Second, nil,
			func(ctx context.Context, _ chan<- fwtrading.Event) (bool, error) {
				select {
				case attempts <- time.Now():
				case <-ctx.Done():
					return false, ctx.Err()
				}
				return false, errors.New("authorization failed")
			}, func(bool, string) {}, func(health.Diagnostic) {},
			func(message string) string { return message })
		if err != nil {
			t.Fatal(err)
		}
		defer s.close()
		previous := <-attempts
		for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
			at := <-attempts
			if got := at.Sub(previous); got != want {
				t.Errorf("retry %d delay = %v, want %v", i+1, got, want)
			}
			previous = at
		}
		s.close()
		if _, ok := <-out; ok {
			t.Fatal("output left open after Close")
		}
	})
}

func TestStreamLifecycleStartStatusMessage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var s streamLifecycle
		var statuses []string
		var inputs []string
		out, err := s.start(context.Background(), streamRefusals{},
			time.Second, 8*time.Second, nil,
			func(context.Context, chan<- fwtrading.Event) (bool, error) {
				return false, errors.New("session error")
			}, func(ok bool, message string) {
				if ok {
					t.Error("failed session reported connected")
				}
				statuses = append(statuses, message)
			}, func(health.Diagnostic) {}, func(message string) string {
				inputs = append(inputs, message)
				return "caller status: " + message
			})
		if err != nil {
			t.Fatal(err)
		}
		defer s.close()
		synctest.Wait()
		s.close()
		if len(inputs) != 1 || inputs[0] != "session error" {
			t.Fatalf("status message inputs = %v, want [session error]", inputs)
		}
		if len(statuses) != 1 || statuses[0] != "caller status: session error" {
			t.Fatalf("status messages = %v, want [caller status: session error]", statuses)
		}
		if _, ok := <-out; ok {
			t.Fatal("output left open after Close")
		}
	})
}
