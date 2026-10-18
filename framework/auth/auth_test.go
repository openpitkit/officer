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

package auth_test

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/domain"
)

func TestCallerFromContextRejectsUnstampedContext(t *testing.T) {
	caller, err := auth.CallerFromContext(context.Background())
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("CallerFromContext on a bare context = (%+v, %v), want ErrInvalid", caller, err)
	}
}

func TestCallerFromContextReturnsStampedSystemCaller(t *testing.T) {
	system := auth.SystemCaller()
	caller, err := auth.CallerFromContext(
		auth.ContextWithCaller(context.Background(), system),
	)
	if err != nil || caller != system {
		t.Fatalf("CallerFromContext on a system-stamped context = (%+v, %v), want (%+v, nil)",
			caller, err, system)
	}
}
