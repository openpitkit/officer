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

import "fmt"

// releaseTag is set by the official image build through -ldflags -X.
// An empty tag identifies local, edge, and dry-run builds of Officer.
var releaseTag string

// buildCommit is set by the binary or image build through -ldflags -X.
var buildCommit string

// ReleaseBuild reports whether Officer was built for an official release tag.
func ReleaseBuild() bool {
	return releaseTag != ""
}

// BuildCommit returns the full lowercase commit ID embedded at build time.
// A non-release build needs it to name its own database file.
func BuildCommit() (string, error) {
	valid := len(buildCommit) == 40 || len(buildCommit) == 64
	for _, ch := range buildCommit {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			valid = false
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("missing or invalid build commit %q: expected 40 or 64 lowercase hexadecimal characters; build through just build (or just build-*), or pass -ldflags \"-X go.openpit.dev/officer/framework/backend.buildCommit=$(git rev-parse HEAD)\" to go build", buildCommit)
	}
	return buildCommit, nil
}
