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
	"errors"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

// credentialStorageTestNode serves trading connections through the trading
// test node, adds market-data sources, and records which lists were read.
type credentialStorageTestNode struct {
	tradingTestNode
	instances []domain.MarketDataInstance
	listed    []string
}

func (n *credentialStorageTestNode) ListTradingConnections(ctx context.Context) ([]domain.TradingConnection, error) {
	n.listed = append(n.listed, "trading")
	return n.tradingTestNode.ListTradingConnections(ctx)
}

func (n *credentialStorageTestNode) ListMarketDataInstances(context.Context) ([]domain.MarketDataInstance, error) {
	n.listed = append(n.listed, "market-data")
	return n.instances, nil
}

func TestCredentialStorage(t *testing.T) {
	connectionWith := func(credentials string) []domain.TradingConnection {
		c := tradingTestConnection()
		c.Credentials = credentials
		return []domain.TradingConnection{c}
	}
	source := func(provider, credentials string) domain.MarketDataInstance {
		return domain.MarketDataInstance{ExternalID: "md-1", Provider: provider, Credentials: credentials}
	}
	sources := func(instances ...domain.MarketDataInstance) []domain.MarketDataInstance {
		return instances
	}
	for _, tc := range []struct {
		name        string
		sealed      bool
		connections []domain.TradingConnection
		instances   []domain.MarketDataInstance
		want        CredentialStorage
		wantErr     string
	}{
		{
			name:        "sealed",
			sealed:      true,
			connections: connectionWith(`{"apiKey":"trading-key-value"}`),
			instances:   sources(source(domain.MarketDataProviderFinnhub, `{"token":"md-token-value"}`)),
			want:        CredentialStorage{Sealed: true},
		},
		{name: "unsealed-empty"},
		{
			name:        "no-keys",
			connections: connectionWith(`{"apiKey":" ","apiSecret":""}`),
			instances: sources(
				source(domain.MarketDataProviderFinnhub, ""),
				source(domain.MarketDataProviderIB, `{"host":"127.0.0.1","port":4002}`),
			),
		},
		{
			name:      "alpaca-api-key",
			instances: sources(source(domain.MarketDataProviderAlpaca, `{"apiKey":"md-key-value"}`)),
			want:      CredentialStorage{PlaintextMarketDataKeys: true},
		},
		{
			name:      "oanda-token",
			instances: sources(source(domain.MarketDataProviderOANDA, `{"token":"md-token-value","accountID":"a"}`)),
			want:      CredentialStorage{PlaintextMarketDataKeys: true},
		},
		{
			name:      "finnhub-token",
			instances: sources(source(domain.MarketDataProviderFinnhub, `{"token":"md-token-value"}`)),
			want:      CredentialStorage{PlaintextMarketDataKeys: true},
		},
		{
			name:        "trading-api-key",
			connections: connectionWith(`{"apiKey":"trading-key-value"}`),
			want:        CredentialStorage{PlaintextTradingKeys: true},
		},
		{
			name:      "keyless-json-null",
			instances: sources(source(domain.MarketDataProviderBinance, `null`)),
		},
		{
			name:      "keyless-json-array",
			instances: sources(source(domain.MarketDataProviderBinance, `[]`)),
		},
		{
			name: "keyless-non-object-beside-keyed-token",
			instances: sources(
				source(domain.MarketDataProviderBinance, `null`),
				source(domain.MarketDataProviderBinance, `[]`),
				source(domain.MarketDataProviderFinnhub, `{"token":"md-token-value"}`),
			),
			want: CredentialStorage{PlaintextMarketDataKeys: true},
		},
		{
			name:      "malformed-market-data",
			instances: sources(source(domain.MarketDataProviderFinnhub, `{"token":"md-token-value"`)),
			wantErr:   "backend: market-data source md-1: stored market-data credentials are not a JSON object: invalid",
		},
		{
			name:      "market-data-json-null",
			instances: sources(source(domain.MarketDataProviderFinnhub, `null`)),
			wantErr:   "backend: market-data source md-1: stored market-data credentials are not a JSON object: invalid",
		},
		{
			name:        "malformed-trading",
			connections: connectionWith(`{"apiKey":"trading-key-value"`),
			wantErr:     "backend: trading connection connection-1: secret: stored credentials are not a JSON object: invalid",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := &credentialStorageTestNode{
				tradingTestNode: tradingTestNode{sealed: tc.sealed, connections: tc.connections},
				instances:       tc.instances,
			}
			got, err := (&Service{node: n}).CredentialStorage(systemCtx())
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr || !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("CredentialStorage error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("CredentialStorage = %+v, want %+v", got, tc.want)
			}
			wantListed := []string{"trading", "market-data"}
			if tc.sealed {
				wantListed = nil
			}
			if !reflect.DeepEqual(n.listed, wantListed) {
				t.Fatalf("listed %v, want %v", n.listed, wantListed)
			}
		})
	}
}
