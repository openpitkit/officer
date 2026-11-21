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

package officer

import (
	"os"
	"path/filepath"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestInitialSetupReviewFundsLimitModes(t *testing.T) {
	for _, mode := range []domain.SpotFundsLimitMode{
		domain.SpotFundsLimitModeReviewWithHold, domain.SpotFundsLimitModeReviewWithoutHold,
	} {
		t.Run(string(mode), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "setup.json")
			body := `{"accountMode":"multi","fundsLimitMode":"` + string(mode) + `","encryption":"disabled","signing":"disabled","access":"open"}`
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			setup, err := LoadInitialSetup(path)
			if err != nil {
				t.Fatal(err)
			}
			if setup.FundsLimitMode != mode {
				t.Fatalf("mode = %q, want %q", setup.FundsLimitMode, mode)
			}
			if err := setup.Validate(false); err != nil {
				t.Fatal(err)
			}
		})
	}
}
