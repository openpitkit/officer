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

package schema

import (
	"slices"
	"testing"
)

func TestAttestationDictionarySeeds(t *testing.T) {
	seeds := make(map[string][]string)
	for _, dictionary := range EnumDictionarySeeds() {
		codes := make([]string, 0, len(dictionary.Codes))
		for _, seed := range dictionary.Codes {
			codes = append(codes, seed.Code)
		}
		seeds[dictionary.Table] = codes
	}

	if got, want := seeds[AttestationAlgTable], []string{"ed25519", "none"}; !slices.Equal(got, want) {
		t.Fatalf("%s codes = %v, want %v", AttestationAlgTable, got, want)
	}
	if got, want := seeds[AttestationModeTable], []string{"immediate", "hold"}; !slices.Equal(got, want) {
		t.Fatalf("%s codes = %v, want %v", AttestationModeTable, got, want)
	}
}
