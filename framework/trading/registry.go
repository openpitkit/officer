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

package trading

import (
	"context"
	"fmt"

	"go.openpit.dev/officer/framework/domain"
)

// Provider describes a connector factory and its destination capabilities.
type Provider struct {
	Type            string
	Title           string
	VenueAccounts   bool
	VerifiesSymbols bool
	Build           func(domain.TradingConnection) (Connector, error)
}

// Registry maps provider types to factories. Register providers before use;
// registration is not concurrent with runtime operations.
type Registry struct{ providers map[string]Provider }

// NewRegistry creates an empty provider registry.
func NewRegistry() *Registry { return &Registry{providers: make(map[string]Provider)} }

// Register adds or replaces a provider. Its type and builder are required.
func (r *Registry) Register(p Provider) error {
	if r == nil || p.Type == "" || p.Build == nil {
		return fmt.Errorf("trading: provider type and builder required: %w", domain.ErrInvalid)
	}
	if r.providers == nil {
		r.providers = make(map[string]Provider)
	}
	r.providers[p.Type] = p
	return nil
}

// Lookup returns a provider by type.
func (r *Registry) Lookup(typ string) (Provider, bool) {
	if r == nil {
		return Provider{}, false
	}
	p, ok := r.providers[typ]
	return p, ok
}

// Build constructs one connection's connector without subscribing.
func (r *Registry) Build(connection domain.TradingConnection) (Connector, error) {
	p, ok := r.Lookup(connection.Provider)
	if !ok {
		return nil, fmt.Errorf("trading: unknown provider %q: %w", connection.Provider, domain.ErrInvalid)
	}
	c, err := p.Build(connection)
	if err != nil {
		return nil, fmt.Errorf("trading: build connector: %w", err)
	}
	if c == nil {
		return nil, fmt.Errorf("trading: provider %q returned no connector: %w", p.Type, domain.ErrInvalid)
	}
	return c, nil
}

// VerifySymbol uses a fresh connector and always closes it. supported=false
// means the connector does not implement SymbolVerifier.
func (r *Registry) VerifySymbol(ctx context.Context, connection domain.TradingConnection, external string) (SymbolVerification, bool, error) {
	c, err := r.Build(connection)
	if err != nil {
		return SymbolVerification{}, false, err
	}
	defer c.Close()
	v, ok := c.(SymbolVerifier)
	if !ok {
		return SymbolVerification{}, false, nil
	}
	result, err := v.VerifySymbol(ctx, external)
	return result, true, err
}
