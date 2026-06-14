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

// Package logtail captures slog output into a bounded in-memory ring buffer and
// exposes it for read-back over the HTTP surface. The buffer is bounded by both
// an entry count and a total byte size so a runaway log loop cannot exhaust
// memory: on append the oldest entries are evicted until both caps hold.
//
// A Handler tees each record into the buffer and delegates to an inner slog
// handler, so the configured stderr output is unchanged while a tail of recent
// lines stays available for the operator dashboard.
package logtail

import (
	"sync"
)

// DefaultMaxEntries is the entry-count cap used when New is given a non-positive
// entry count.
const DefaultMaxEntries = 2000

// DefaultMaxBytes is the total-bytes cap used when New is given a non-positive
// byte size. It bounds the buffer at 2 MiB of formatted log text.
const DefaultMaxBytes = 2 << 20

// Buffer is a thread-safe bounded ring buffer of formatted log lines. slog
// handlers are invoked concurrently, so every access is guarded by a mutex. It
// is bounded by both a maximum entry count and a maximum total byte size; an
// append evicts the oldest entries until the buffer is within both caps.
type Buffer struct {
	mu         sync.Mutex
	lines      []string
	bytes      int
	maxEntries int
	maxBytes   int
}

// New returns a buffer bounded by maxEntries lines and maxBytes total bytes. A
// non-positive bound falls back to its default (DefaultMaxEntries,
// DefaultMaxBytes).
func New(maxEntries int, maxBytes int) *Buffer {
	if maxEntries <= 0 {
		maxEntries = DefaultMaxEntries
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	return &Buffer{maxEntries: maxEntries, maxBytes: maxBytes}
}

// Append adds one formatted line, then evicts the oldest entries until the
// buffer is within both the entry-count and total-bytes caps. A single line
// larger than the byte cap is still retained as the sole entry so the most
// recent record is never silently dropped.
func (b *Buffer) Append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lines = append(b.lines, line)
	b.bytes += len(line)

	// Evict oldest until within both caps; keep at least the just-appended line.
	for len(b.lines) > 1 &&
		(len(b.lines) > b.maxEntries || b.bytes > b.maxBytes) {
		b.bytes -= len(b.lines[0])
		b.lines = b.lines[1:]
	}
}

// Snapshot returns the buffered lines oldest to newest. The returned slice is a
// copy, so callers may retain or mutate it without affecting the buffer.
func (b *Buffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
