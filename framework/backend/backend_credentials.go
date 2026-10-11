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
	"context"
	"fmt"

	"go.openpit.dev/officer/framework/secret"
)

// CredentialStorage reports how stored credentials sit at rest. Both plaintext
// flags are false whenever Sealed is true.
type CredentialStorage struct {
	// Sealed is true when credentials written now are sealed under the
	// master key.
	Sealed bool
	// PlaintextTradingKeys is true when credentials are not sealed and at
	// least one trading connection holds a key.
	PlaintextTradingKeys bool
	// PlaintextMarketDataKeys is true when credentials are not sealed and
	// at least one market-data source holds a key.
	PlaintextMarketDataKeys bool
}

// CredentialStorage reports whether stored credentials are sealed under the
// master key and, when they are not, whether any trading connection or
// market-data source holds a key in plain text. It reads the node only, so it
// needs no trading runtime. Stored trading credentials that are not a JSON
// object fail the call; market-data credentials fail it only for a provider
// that has a secret (see MarketDataSecrets).
func (s *Service) CredentialStorage(ctx context.Context) (CredentialStorage, error) {
	if s.node.CredentialsSealed() {
		return CredentialStorage{Sealed: true}, nil
	}
	var storage CredentialStorage
	connections, err := s.node.ListTradingConnections(ctx)
	if err != nil {
		return CredentialStorage{}, fmt.Errorf("backend: list trading connections: %w", err)
	}
	for _, c := range connections {
		secrets, _, err := secret.CredentialRedactor(c.Credentials)
		if err != nil {
			return CredentialStorage{}, fmt.Errorf("backend: trading connection %s: %w", c.ExternalID, err)
		}
		if holdsSecret(secrets) {
			storage.PlaintextTradingKeys = true
		}
	}
	instances, err := s.node.ListMarketDataInstances(ctx)
	if err != nil {
		return CredentialStorage{}, fmt.Errorf("backend: list market-data instances: %w", err)
	}
	for _, instance := range instances {
		secrets, err := MarketDataSecrets(instance)
		if err != nil {
			return CredentialStorage{}, fmt.Errorf("backend: market-data source %s: %w", instance.ExternalID, err)
		}
		if holdsSecret(secrets) {
			storage.PlaintextMarketDataKeys = true
		}
	}
	return storage, nil
}

// holdsSecret reports whether a credential presence map marks any credential
// as set.
func holdsSecret(secrets map[string]bool) bool {
	for _, present := range secrets {
		if present {
			return true
		}
	}
	return false
}
