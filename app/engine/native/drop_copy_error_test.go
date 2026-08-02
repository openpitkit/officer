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
	"testing"
)

func TestSubmitOrder_DropCopyMarketOrderReturnsPolicyReject(t *testing.T) {
	e := newTestEngine(t)
	order := testOrder()
	order.DropCopy = true
	order.Price = ""

	result, err := e.SubmitOrder(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitOrder(drop-copy market): %v", err)
	}
	if result.Accepted || len(result.Rejects) != 1 {
		t.Fatalf(
			"drop-copy market result = %+v, want one policy reject", result,
		)
	}
	if result.Rejects[0].Code != "missing_required_field" {
		t.Fatalf(
			"drop-copy market reject = %+v, want missing_required_field",
			result.Rejects[0],
		)
	}
}
