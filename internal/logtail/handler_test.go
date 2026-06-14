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
// Please see https://openpit.dev and the OWNERS file for details.

package logtail

import (
	"context"
	"log/slog"
	"strings"
	"testing"
)

// fakeInner is a minimal slog.Handler that records the messages it was asked to
// handle, so a test can assert the tee delegated to it. WithAttrs/WithGroup
// return the same instance so all delegated records land in one place.
type fakeInner struct {
	handled *[]string
}

func newFakeInner() *fakeInner {
	return &fakeInner{handled: &[]string{}}
}

func (f *fakeInner) Enabled(context.Context, slog.Level) bool { return true }

func (f *fakeInner) Handle(_ context.Context, record slog.Record) error {
	*f.handled = append(*f.handled, record.Message)
	return nil
}

func (f *fakeInner) WithAttrs([]slog.Attr) slog.Handler { return f }

func (f *fakeInner) WithGroup(string) slog.Handler { return f }

// TestHandlerTeesToBufferAndInner checks Handle both appends a formatted line to
// the buffer and delegates the record to the inner handler.
func TestHandlerTeesToBufferAndInner(t *testing.T) {
	buf := New(0, 0)
	inner := newFakeInner()
	logger := slog.New(NewHandler(inner, buf))

	logger.Info("engine built", "version", "v1.2.3")

	if len(*inner.handled) != 1 || (*inner.handled)[0] != "engine built" {
		t.Fatalf("want inner to receive the record, got %v", *inner.handled)
	}

	snap := buf.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 buffered line, got %d (%v)", len(snap), snap)
	}
	line := snap[0]
	if !strings.Contains(line, "engine built") {
		t.Fatalf("buffered line missing message: %q", line)
	}
	if !strings.Contains(line, "INFO") {
		t.Fatalf("buffered line missing level: %q", line)
	}
	if !strings.Contains(line, "version=v1.2.3") {
		t.Fatalf("buffered line missing record attr: %q", line)
	}
}

// TestHandlerWithAttrsSharesBufferAndIncludesAttrs checks a WithAttrs-derived
// handler writes into the SAME buffer and that handler-bound attrs appear in the
// formatted line.
func TestHandlerWithAttrsSharesBufferAndIncludesAttrs(t *testing.T) {
	buf := New(0, 0)
	inner := newFakeInner()
	base := slog.New(NewHandler(inner, buf))

	child := base.With("node", "local-1")
	child.Warn("slow tick")

	snap := buf.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 buffered line in shared buffer, got %d (%v)", len(snap), snap)
	}
	line := snap[0]
	if !strings.Contains(line, "slow tick") {
		t.Fatalf("buffered line missing message: %q", line)
	}
	if !strings.Contains(line, "node=local-1") {
		t.Fatalf("buffered line missing WithAttrs attr: %q", line)
	}
}

// TestHandlerWithGroupQualifiesKeys checks WithGroup shares the buffer and
// renders subsequent record attrs under the dotted group path.
func TestHandlerWithGroupQualifiesKeys(t *testing.T) {
	buf := New(0, 0)
	inner := newFakeInner()
	base := slog.New(NewHandler(inner, buf))

	base.WithGroup("market").Info("quote", "symbol", "BTCUSD")

	snap := buf.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 buffered line, got %d (%v)", len(snap), snap)
	}
	if !strings.Contains(snap[0], "market.symbol=BTCUSD") {
		t.Fatalf("buffered line missing grouped attr: %q", snap[0])
	}
}
