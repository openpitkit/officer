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

// Package logtest holds helpers that restore process-wide logging after tests.
package logtest

import (
	"log"
	"log/slog"
	"testing"
)

// SetDefault installs h as the default slog handler and restores the previous
// logger, standard log writer and flags when t ends. It changes process-global
// state, so a test using it must not run in parallel with another test that logs
// or captures logs.
func SetDefault(t testing.TB, h slog.Handler) {
	t.Helper()
	previous := slog.Default()
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() {
		slog.SetDefault(previous)
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
}
