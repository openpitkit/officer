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
	"fmt"
	"strings"
	"testing"
)

// TestNewDefaults checks non-positive bounds fall back to the exported
// defaults: the buffer keeps up to DefaultMaxEntries lines.
func TestNewDefaults(t *testing.T) {
	buf := New(0, -1)
	for i := 0; i < DefaultMaxEntries+50; i++ {
		buf.Append(fmt.Sprintf("line-%d", i))
	}
	if got := len(buf.Snapshot()); got != DefaultMaxEntries {
		t.Fatalf("want %d lines, got %d", DefaultMaxEntries, got)
	}
}

// TestEvictByEntryCount checks the oldest lines are evicted once the entry-count
// cap is exceeded, keeping the most recent maxEntries.
func TestEvictByEntryCount(t *testing.T) {
	buf := New(3, 1<<20)
	for i := 0; i < 5; i++ {
		buf.Append(fmt.Sprintf("line-%d", i))
	}
	got := buf.Snapshot()
	want := []string{"line-2", "line-3", "line-4"}
	if len(got) != len(want) {
		t.Fatalf("want %d lines, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

// TestEvictByBytes checks eviction by total byte size: with a tiny byte cap and
// a generous entry cap, appending past the cap drops the oldest lines.
func TestEvictByBytes(t *testing.T) {
	// Each line is 6 bytes ("aaaaa\n"? no) - use fixed 5-byte payloads.
	buf := New(1000, 12) // room for ~2 of the 5-byte lines plus slack
	buf.Append("aaaaa")  // 5
	buf.Append("bbbbb")  // 10
	buf.Append("ccccc")  // 15 > 12 -> evict "aaaaa" -> 10
	got := buf.Snapshot()
	want := []string{"bbbbb", "ccccc"}
	if len(got) != len(want) {
		t.Fatalf("want %d lines, got %d (%v)", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d: want %q, got %q", i, want[i], got[i])
		}
	}
}

// TestEvictByBytesKeepsLastOversizedLine checks a single line larger than the
// byte cap is retained as the sole entry rather than being dropped.
func TestEvictByBytesKeepsLastOversizedLine(t *testing.T) {
	buf := New(1000, 4)
	buf.Append("short")
	buf.Append(strings.Repeat("x", 100))
	got := buf.Snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 line retained, got %d (%v)", len(got), got)
	}
	if got[0] != strings.Repeat("x", 100) {
		t.Fatalf("want the oversized line retained, got %q", got[0])
	}
}

// TestSnapshotOrderAndCopy checks Snapshot returns lines oldest-to-newest and
// that the returned slice is a copy: mutating it does not affect the buffer.
func TestSnapshotOrderAndCopy(t *testing.T) {
	buf := New(10, 1<<20)
	buf.Append("first")
	buf.Append("second")

	snap := buf.Snapshot()
	if snap[0] != "first" || snap[1] != "second" {
		t.Fatalf("want oldest-first order, got %v", snap)
	}

	// Mutate the returned copy and append more; a fresh snapshot must be intact.
	snap[0] = "tampered"
	buf.Append("third")

	again := buf.Snapshot()
	want := []string{"first", "second", "third"}
	if len(again) != len(want) {
		t.Fatalf("want %d lines, got %d (%v)", len(want), len(again), again)
	}
	for i := range want {
		if again[i] != want[i] {
			t.Fatalf("line %d: want %q, got %q", i, want[i], again[i])
		}
	}
}
