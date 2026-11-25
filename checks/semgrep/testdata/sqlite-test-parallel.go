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

package testdata

import (
	"log/slog"
	"os"
	"syscall"
	"testing"

	"go.openpit.dev/officer/internal/logtest"
)

// ruleid: sqlite-test-parallel
func TestSerial(t *testing.T) {
	t.Log("runs serially")
}

// ruleid: sqlite-test-parallel
func TestLateParallel(t *testing.T) {
	t.Log("starts serially")
	t.Parallel()
}

// ruleid: sqlite-test-parallel
func TestOnlySubtestIsParallel(t *testing.T) {
	t.Run("case", func(t *testing.T) {
		t.Parallel()
	})
}

// ruleid: sqlite-test-parallel
func TestMentionsGlobalCall(t *testing.T) {
	t.Log("syscall.Umask(0o022)")
}

// ok: sqlite-test-parallel
func TestIndependent(test *testing.T) {
	test.Parallel()
	test.Log("runs in parallel")
}

// ok: sqlite-test-parallel
func TestChangesEnvironment(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
}

// ok: sqlite-test-parallel
func TestChangesWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
}

// ok: sqlite-test-parallel
func TestUnsetsEnvironment(t *testing.T) {
	_ = os.Unsetenv("TMPDIR")
}

// ok: sqlite-test-parallel
func TestClearsEnvironment(t *testing.T) {
	os.Clearenv()
}

// ok: sqlite-test-parallel
func TestChangesMaskInSubtest(t *testing.T) {
	t.Run("case", func(t *testing.T) {
		previous := syscall.Umask(0o022)
		t.Cleanup(func() { syscall.Umask(previous) })
	})
}

// ok: sqlite-test-parallel
func TestChangesDefaultLogger(t *testing.T) {
	logtest.SetDefault(t, slog.NewTextHandler(os.Stderr, nil))
}

// ok: sqlite-test-parallel
func TestSetsDefaultLoggerDirectly(t *testing.T) {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

// ok: sqlite-test-parallel
func TestCapturesDefaultLogger(t *testing.T) {
	captureSlogForTest(t)
}

func TestParallelChangesEnvironment(t *testing.T) {
	t.Parallel()
	// ruleid: sqlite-test-parallel-global-state
	t.Setenv("TMPDIR", t.TempDir())
}

func TestParallelChangesWorkingDirectory(t *testing.T) {
	t.Parallel()
	// ruleid: sqlite-test-parallel-global-state
	t.Chdir(t.TempDir())
}

func TestParallelUnsetsEnvironment(t *testing.T) {
	t.Parallel()
	// ruleid: sqlite-test-parallel-global-state
	_ = os.Unsetenv("TMPDIR")
}

func TestParallelClearsEnvironment(t *testing.T) {
	t.Parallel()
	// ruleid: sqlite-test-parallel-global-state
	os.Clearenv()
}

func TestParallelChangesMaskInSubtest(t *testing.T) {
	t.Parallel()
	t.Run("case", func(t *testing.T) {
		// ruleid: sqlite-test-parallel-global-state
		previous := syscall.Umask(0o022)
		// ruleid: sqlite-test-parallel-global-state
		defer syscall.Umask(previous)
	})
}

func TestParallelChangesDefaultLogger(t *testing.T) {
	t.Parallel()
	// ruleid: sqlite-test-parallel-global-state
	logtest.SetDefault(t, slog.NewTextHandler(os.Stderr, nil))
}

func TestParallelSetsDefaultLoggerDirectly(t *testing.T) {
	t.Parallel()
	// ruleid: sqlite-test-parallel-global-state
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
}

func TestLateParallelChangesDefaultLogger(t *testing.T) {
	// ruleid: sqlite-test-parallel-global-state
	captureSlogForTest(t)
	t.Parallel()
}

func TestParallelMentionsGlobalCall(t *testing.T) {
	t.Parallel()
	// ok: sqlite-test-parallel-global-state
	t.Log("syscall.Umask(0o022)")
}

func TestSerialChangesDefaultLogger(t *testing.T) {
	// ok: sqlite-test-parallel-global-state
	captureSlogForTest(t)
}

func captureSlogForTest(t *testing.T) {
	t.Helper()
	logtest.SetDefault(t, slog.NewTextHandler(os.Stderr, nil))
}

// ok: sqlite-test-parallel
func testHelper(t *testing.T) {
	t.Helper()
}
