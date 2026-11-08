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
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/coder/websocket"
)

func closeStream(name string, conn interface {
	Close(websocket.StatusCode, string) error
}, redact ...func(error) error) {
	err := conn.Close(websocket.StatusNormalClosure, "")
	// coder/websocket returns net.ErrClosed on repeated Close, including after a peer close frame.
	if err == nil || errors.Is(err, net.ErrClosed) {
		return
	}
	for _, replace := range redact {
		err = replace(err)
	}
	slog.Warn(name+": close WebSocket stream", "error", err)
}

func readStreamFrame(
	ctx context.Context,
	timeout time.Duration,
	read func(context.Context) ([]byte, error),
) ([]byte, error) {
	if timeout <= 0 {
		return read(ctx)
	}
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	payload, err := read(readCtx)
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("stream idle timeout after %s", timeout)
	}
	return payload, err
}
