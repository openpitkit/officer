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
	"fmt"

	"go.openpit.dev/officer/framework/domain"
)

// Provider describes one registerable market-data connector kind.
type Provider struct {
	// Type is the stable provider discriminator.
	Type string
	// Title is the human-readable catalogue label.
	Title string
	// VerifiesSymbols declares SymbolVerifier support without building.
	VerifiesSymbols bool
	// SearchesSymbols declares SymbolSearcher support without building.
	SearchesSymbols bool
	// Build constructs one connector from the full instance config.
	Build func(domain.MarketDataInstance) (Connector, error)
}

// Registry holds registered providers in insertion order.
type Registry struct {
	providers map[string]Provider
	order     []string
}

// NewRegistry builds an empty provider registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]Provider)}
}

// Register adds or replaces a provider.
//
// Duplicate Type replaces in place, preserving insertion order; Unregister
// explicitly hides a provider.
func (r *Registry) Register(p Provider) error {
	if p.Type == "" {
		return fmt.Errorf("marketdata: provider type is empty")
	}
	if r.providers == nil {
		r.providers = make(map[string]Provider)
	}
	if _, ok := r.providers[p.Type]; !ok {
		r.order = append(r.order, p.Type)
	}
	r.providers[p.Type] = p
	return nil
}

// Unregister removes a provider by Type and reports whether it was present.
func (r *Registry) Unregister(typ string) bool {
	if r == nil || r.providers == nil {
		return false
	}
	if _, ok := r.providers[typ]; !ok {
		return false
	}
	delete(r.providers, typ)
	for i, ordered := range r.order {
		if ordered == typ {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return true
}

// Lookup returns the descriptor registered for typ.
func (r *Registry) Lookup(typ string) (Provider, bool) {
	if r == nil {
		return Provider{}, false
	}
	p, ok := r.providers[typ]
	return p, ok
}

// Build constructs the connector for instance.
func (r *Registry) Build(instance domain.MarketDataInstance) (Connector, error) {
	p, ok := r.Lookup(instance.Provider)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedProvider, instance.Provider)
	}
	if p.Build == nil {
		return nil, fmt.Errorf("marketdata: provider %q has no builder", p.Type)
	}
	return p.Build(instance)
}

// List returns the registered providers in insertion order.
//
// This is the single source the backend catalogue will be derived from later.
func (r *Registry) List() []Provider {
	if r == nil {
		return nil
	}
	out := make([]Provider, 0, len(r.order))
	for _, typ := range r.order {
		if p, ok := r.providers[typ]; ok {
			out = append(out, p)
		}
	}
	return out
}

// Title returns the registered human label for typ.
func (r *Registry) Title(typ string) (string, bool) {
	p, ok := r.Lookup(typ)
	if !ok {
		return "", false
	}
	return p.Title, true
}

// Known reports whether typ is registered.
func (r *Registry) Known(typ string) bool {
	_, ok := r.Lookup(typ)
	return ok
}

// VerifiesSymbols reports the declared symbol-verification capability.
func (r *Registry) VerifiesSymbols(typ string) bool {
	p, ok := r.Lookup(typ)
	return ok && p.VerifiesSymbols
}

// SearchesSymbols reports the declared symbol-search capability.
func (r *Registry) SearchesSymbols(typ string) bool {
	p, ok := r.Lookup(typ)
	return ok && p.SearchesSymbols
}

// Validate checks whether instance can build a provider connector.
func (r *Registry) Validate(instance domain.MarketDataInstance) error {
	connector, err := r.Build(instance)
	if err != nil {
		return err
	}
	connector.Close()
	return nil
}

// VerifySymbol checks one symbol with a fresh connector and closes it.
func (r *Registry) VerifySymbol(
	ctx context.Context, instance domain.MarketDataInstance, external string,
) (result SymbolVerification, supported bool, err error) {
	connector, err := r.Build(instance)
	if err != nil {
		return SymbolVerification{}, false, err
	}
	defer connector.Close()

	verifier, ok := connector.(SymbolVerifier)
	if !ok {
		return SymbolVerification{}, false, nil
	}
	result, err = verifier.VerifySymbol(ctx, external)
	if err != nil {
		return SymbolVerification{}, true, err
	}
	return result, true, nil
}

// SearchSymbols searches symbols with a fresh connector and closes it.
func (r *Registry) SearchSymbols(
	ctx context.Context, instance domain.MarketDataInstance, query SymbolSearchQuery,
) (matches []SymbolMatch, supported bool, err error) {
	connector, err := r.Build(instance)
	if err != nil {
		return nil, false, err
	}
	defer connector.Close()

	searcher, ok := connector.(SymbolSearcher)
	if !ok {
		return nil, false, nil
	}
	matches, err = searcher.SearchSymbols(ctx, query)
	if err != nil {
		return nil, true, err
	}
	return matches, true, nil
}
