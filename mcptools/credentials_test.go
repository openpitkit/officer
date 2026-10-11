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

package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/backend"
	frameworkmcp "go.openpit.dev/officer/framework/mcp"
)

// credentialStorageSource serves CredentialStorage only; any other Source call
// panics on the nil embedded interface.
type credentialStorageSource struct {
	frameworkmcp.Source
	storage backend.CredentialStorage
	err     error
}

func (s credentialStorageSource) CredentialStorage(context.Context) (backend.CredentialStorage, error) {
	return s.storage, s.err
}

func TestGetCredentialStorageTool(t *testing.T) {
	for _, tc := range []struct {
		name    string
		storage backend.CredentialStorage
		want    string
	}{
		{"sealed", backend.CredentialStorage{Sealed: true}, `{"credentialsSealed":true,"plaintextTradingKeys":false,"plaintextMarketDataKeys":false}`},
		{"plaintext-trading-keys", backend.CredentialStorage{PlaintextTradingKeys: true}, `{"credentialsSealed":false,"plaintextTradingKeys":true,"plaintextMarketDataKeys":false}`},
		{"plaintext-market-data-keys", backend.CredentialStorage{PlaintextMarketDataKeys: true}, `{"credentialsSealed":false,"plaintextTradingKeys":false,"plaintextMarketDataKeys":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handler := getCredentialStorageHandler(credentialStorageSource{storage: tc.storage})
			_, out, err := handler(t.Context(), nil, struct{}{})
			if err != nil {
				t.Fatal(err)
			}
			wire, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			if string(wire) != tc.want {
				t.Fatalf("output = %s, want %s", wire, tc.want)
			}
		})
	}
	t.Run("source-error", func(t *testing.T) {
		handler := getCredentialStorageHandler(credentialStorageSource{err: errors.New("store unavailable")})
		_, _, err := handler(t.Context(), nil, struct{}{})
		if err == nil || err.Error() != "internal: internal error" {
			t.Fatalf("error = %v, want internal: internal error, as REST answers", err)
		}
	})
}
