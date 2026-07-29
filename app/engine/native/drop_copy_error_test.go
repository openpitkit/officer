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

package native

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestSubmitOrder_DropCopyMarketOrderReturnsInvalidInput(t *testing.T) {
	e := newTestEngine(t)
	order := testOrder()
	order.DropCopy = true
	order.Price = ""

	_, err := e.SubmitOrder(context.Background(), order)
	if err == nil {
		t.Fatal("SubmitOrder(drop-copy market): want admission error")
	}
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("SubmitOrder(drop-copy market) error = %v, want ErrInvalid", err)
	}
	if !strings.Contains(err.Error(), "failed to access field 'limit price'") {
		t.Fatalf("SubmitOrder(drop-copy market) error = %v", err)
	}
}
