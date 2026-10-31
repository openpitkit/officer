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

package backend

import (
	"strings"
	"testing"
)

func TestBuildCommitMissing(t *testing.T) {
	original := buildCommit
	t.Cleanup(func() { buildCommit = original })
	buildCommit = ""

	commit, err := BuildCommit()
	if err == nil {
		t.Fatal("BuildCommit() accepted a missing commit")
	}
	if commit != "" {
		t.Fatalf("BuildCommit() = %q on error, want empty", commit)
	}
	for _, hint := range []string{
		"just build",
		`-ldflags "-X go.openpit.dev/officer/framework/backend.buildCommit=$(git rev-parse HEAD)"`,
	} {
		if !strings.Contains(err.Error(), hint) {
			t.Errorf("BuildCommit() error %q does not contain %q", err, hint)
		}
	}
}

func TestBuildCommitNonHex(t *testing.T) {
	original := buildCommit
	t.Cleanup(func() { buildCommit = original })
	for _, tt := range []struct {
		name   string
		commit string
	}{
		{"short", "abc1234"},
		{"uppercase", strings.Repeat("A", 40)},
		{"non-hex", strings.Repeat("a", 39) + "g"},
		{"non-hex-rune", strings.Repeat("a", 38) + "é"},
		{"length-41", strings.Repeat("a", 41)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			buildCommit = tt.commit
			commit, err := BuildCommit()
			if err == nil {
				t.Fatalf("BuildCommit() accepted invalid commit %q", tt.commit)
			}
			if commit != "" {
				t.Fatalf("BuildCommit() = %q on error, want empty", commit)
			}
		})
	}
}

func TestBuildCommitValid(t *testing.T) {
	original := buildCommit
	t.Cleanup(func() { buildCommit = original })
	for _, commit := range []string{
		"0123456789abcdef0123456789abcdef01234567",
		strings.Repeat("0123456789abcdef", 4),
	} {
		t.Run(commit, func(t *testing.T) {
			buildCommit = commit
			got, err := BuildCommit()
			if err != nil || got != commit {
				t.Fatalf("BuildCommit() = (%q, %v), want (%q, nil)", got, err, commit)
			}
		})
	}
}

func TestReleaseBuild(t *testing.T) {
	original := releaseTag
	t.Cleanup(func() { releaseTag = original })
	for _, tag := range []string{"", "v0.1.0"} {
		releaseTag = tag
		if got, want := ReleaseBuild(), tag != ""; got != want {
			t.Errorf("ReleaseBuild() with tag %q = %t, want %t", tag, got, want)
		}
	}
}
