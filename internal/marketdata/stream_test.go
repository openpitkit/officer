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

package marketdata

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.openpit.dev/officer/internal/logtest"
)

type streamCloseFunc func(websocket.StatusCode, string) error

func (close streamCloseFunc) Close(code websocket.StatusCode, reason string) error {
	return close(code, reason)
}

func TestCloseStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		warn bool
	}{
		{name: "nil"},
		{name: "already closed", err: net.ErrClosed},
		{name: "wrapped already closed", err: fmt.Errorf("failed to close WebSocket: %w", net.ErrClosed)},
		{name: "failure", err: errors.New("close failure"), warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logtest.SetDefault(t, slog.NewTextHandler(&logs, nil))

			called := false
			closeStream("test venue", streamCloseFunc(func(code websocket.StatusCode, reason string) error {
				called = true
				if code != websocket.StatusNormalClosure || reason != "" {
					t.Errorf("Close(%v, %q), want normal closure with empty reason", code, reason)
				}
				return tc.err
			}))
			if !called {
				t.Fatal("stream was not closed")
			}
			if !tc.warn {
				if logs.Len() != 0 {
					t.Fatalf("unexpected log: %s", logs.String())
				}
				return
			}
			for _, want := range []string{"level=WARN", "test venue: close WebSocket stream", tc.err.Error()} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("log = %q, want %q", logs.String(), want)
				}
			}
		})
	}
}

func TestCloseStreamRedactsError(t *testing.T) {
	var logs bytes.Buffer
	logtest.SetDefault(t, slog.NewTextHandler(&logs, nil))

	const token = "secret-token"
	closeStream("finnhub", streamCloseFunc(func(websocket.StatusCode, string) error {
		return errors.New("close failed: " + token)
	}), func(err error) error {
		return redactFinnhubStreamError(err, token)
	})
	if strings.Contains(logs.String(), token) || !strings.Contains(logs.String(), "REDACTED") {
		t.Fatalf("log = %q, want redacted close failure", logs.String())
	}
}

func TestReadStreamFrameIdleTimeout(t *testing.T) {
	t.Parallel()

	_, err := readStreamFrame(
		context.Background(),
		time.Millisecond,
		func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	if err == nil || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("err = %v, want stream idle timeout", err)
	}
}
