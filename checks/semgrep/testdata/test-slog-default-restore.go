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
	. "log/slog"
	s "log/slog"
	"testing"

	"go.openpit.dev/officer/internal/logtest"
)

func captureDefaultDirectly(t *testing.T, handler slog.Handler) {
	previous := slog.Default()
	// ruleid: test-slog-default-restore
	slog.SetDefault(slog.New(handler))
	// ruleid: test-slog-default-restore
	defer slog.SetDefault(previous)
	t.Cleanup(func() {
		// ruleid: test-slog-default-restore
		slog.SetDefault(previous)
	})
}

func captureDefaultWithCleanup(t *testing.T, handler slog.Handler) {
	// ok: test-slog-default-restore
	logtest.SetDefault(t, handler)
	// ok: test-slog-default-restore
	_ = slog.Default()
}

func captureAliasedDefaultDirectly(t *testing.T, handler s.Handler) {
	previous := s.Default()
	// ruleid: test-slog-default-restore
	s.SetDefault(s.New(handler))
	// ruleid: test-slog-default-restore
	defer s.SetDefault(previous)
	t.Cleanup(func() {
		// ruleid: test-slog-default-restore
		s.SetDefault(previous)
	})
}

func captureAliasedDefaultWithCleanup(t *testing.T, handler s.Handler) {
	// ok: test-slog-default-restore
	logtest.SetDefault(t, handler)
	// ok: test-slog-default-restore
	_ = s.Default()
}

func captureDotDefaultDirectly(t *testing.T, handler Handler) {
	previous := Default()
	// ruleid: test-slog-default-restore
	SetDefault(New(handler))
	// ruleid: test-slog-default-restore
	defer SetDefault(previous)
	t.Cleanup(func() {
		// ruleid: test-slog-default-restore
		SetDefault(previous)
	})
}

func captureDotDefaultWithCleanup(t *testing.T, handler Handler) {
	// ok: test-slog-default-restore
	logtest.SetDefault(t, handler)
	// ok: test-slog-default-restore
	_ = Default()
}
