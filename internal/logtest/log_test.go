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

package logtest

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestSetDefaultRestoresLogging(t *testing.T) {
	previous := slog.Default()
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	var restoredLogs bytes.Buffer
	const flags = log.Ldate | log.Ltime | log.Lmicroseconds
	log.SetOutput(&restoredLogs)
	log.SetFlags(flags)

	t.Run("capture", func(t *testing.T) {
		var captured bytes.Buffer
		handler := slog.NewTextHandler(&captured, nil)
		SetDefault(t, handler)
		if slog.Default().Handler() != handler {
			t.Fatal("default logger does not use the supplied handler")
		}
		slog.Info("captured slog message")
		log.Print("captured log message")
		for _, want := range []string{"captured slog message", "captured log message"} {
			if !strings.Contains(captured.String(), want) {
				t.Errorf("captured logs = %q, want %q", captured.String(), want)
			}
		}
	})

	if slog.Default() != previous {
		t.Error("default logger was not restored")
	}
	if log.Writer() != &restoredLogs {
		t.Error("standard log writer was not restored")
	}
	if got := log.Flags(); got != flags {
		t.Errorf("standard log flags = %d, want %d", got, flags)
	}
}
