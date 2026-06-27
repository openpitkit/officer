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

package signing

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestServiceOrUnavailableReturnsNilSafeService(t *testing.T) {
	t.Parallel()

	service := ServiceOrUnavailable(nil)
	if !IsUnavailable(service) {
		t.Fatal("ServiceOrUnavailable(nil) was not marked unavailable")
	}

	if _, err := service.GenerateKey(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("GenerateKey = %v, want ErrNotConfigured", err)
	}
	if _, err := service.Sign(domain.ApprovalPayload{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Sign = %v, want ErrNotConfigured", err)
	}
	if _, err := service.NoESign(context.Background()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NoESign = %v, want ErrNotConfigured", err)
	}
	if _, err := service.Verify(context.Background(), "", VerifyParams{}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Verify = %v, want ErrNotConfigured", err)
	}
	if !errors.Is(ErrNotConfigured, domain.ErrNotImplemented) {
		t.Fatalf("ErrNotConfigured must wrap ErrNotImplemented")
	}
}
