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

package marketdata

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestReadStreamFrameIdleTimeout(t *testing.T) {
	t.Parallel()

	_, err := readStreamFrame(
		context.Background(),
		time.Millisecond,
		func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	)
	if err == nil || !strings.Contains(err.Error(), "stream idle timeout") {
		t.Fatalf("err = %v, want stream idle timeout", err)
	}
}
