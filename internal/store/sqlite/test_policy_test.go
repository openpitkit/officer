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

package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/framework/trading"
)

func newClassifierStore(t *testing.T) store.Store {
	t.Helper()
	st, err := New(filepath.Join(t.TempDir(), "classifier.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestTradingProviderClassifierBinding(t *testing.T) {
	st := newClassifierStore(t)
	if err := st.BindExecutionPolicyProviders(nil); err == nil {
		t.Fatal("nil classifier accepted")
	}
	registry := trading.NewRegistry()
	if err := st.BindExecutionPolicyProviders(registry.SupportsExecutionPolicy); err != nil {
		t.Fatal(err)
	}
	if err := st.BindExecutionPolicyProviders(registry.SupportsExecutionPolicy); err == nil {
		t.Fatal("second binding accepted")
	}
}

func TestTestExecutionPolicyClassificationAcrossHandles(t *testing.T) {
	ctx := context.Background()
	st := newClassifierStore(t)
	before, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewExternalID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := before.GetTestExecutionPolicy(ctx, id); err == nil || !strings.Contains(err.Error(), "execution-policy provider lookup is not bound") {
		t.Fatalf("unbound policy read: %v", err)
	}
	registry := trading.NewRegistry()
	if err := registry.Register(trading.Provider{Type: "venue", ExecutionPolicy: false, Build: func(trading.BuildContext) (trading.Connector, error) { return nil, nil }}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindExecutionPolicyProviders(registry.SupportsExecutionPolicy); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, realm store.RealmStore, suffix string) {
		t.Helper()
		for _, tc := range []struct {
			provider string
			want     error
		}{{"venue", domain.ErrExecutionPolicyUnsupported}, {"unregistered", domain.ErrTradingProviderUnavailable}} {
			connection, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{Provider: "venue", Label: tc.provider + suffix, Mode: domain.TradingModePaper, VenueIdentity: tc.provider + suffix, Credentials: "{}"})
			if err != nil {
				t.Fatal(err)
			}
			// Corrupt the stored provider to exercise read-time classification;
			// create now refuses unregistered providers before inserting them.
			if _, err := realm.(*realmStore).rawDB().ExecContext(ctx,
				`UPDATE trading_connection SET provider = ? WHERE external_id = ?`, tc.provider, connection.ExternalID.Bytes(),
			); err != nil {
				t.Fatal(err)
			}
			if _, err := realm.GetTestExecutionPolicy(ctx, connection.ExternalID); !errors.Is(err, tc.want) {
				t.Fatalf("%s read: %v, want %v", tc.provider, err, tc.want)
			}
		}
		if _, err := realm.GetTestExecutionPolicy(ctx, id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown connection: %v", err)
		}
	}
	check(t, before, "-before-bind-handle")
	after, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	check(t, after, "-new-handle")
	if err := st.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	check(t, before, "-old-after-reset")
	reset, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	check(t, reset, "-new-after-reset")
	if err := st.BindExecutionPolicyProviders(registry.SupportsExecutionPolicy); err == nil {
		t.Fatal("reset lost the binding")
	}
}

func TestDecodeTestExecutionPolicy(t *testing.T) {
	valid := `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal"}]`
	for _, tc := range []struct {
		name      string
		rules     string
		wantError string
		wantRules int
	}{
		{"unknown field", `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal","extra":true}]`, "unknown field", 0},
		{"unknown wait field", `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"rest","wait":{"kind":"infinite","extra":true}}]`, "unknown field", 0},
		{"case alias", `[{"mask":{"BaseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal"}]`, "unknown field", 0},
		{"null fills", `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal","fills":null}]`, "fills must not be null", 0},
		{"null wait", `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"execute","priceVariant":"normal","wait":null}]`, "wait must not be null", 0},
		{"null price", `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"rest","priceVariant":null,"wait":{"kind":"infinite"}}]`, "priceVariant must not be null", 0},
		{"null seconds", `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"rest","wait":{"kind":"infinite","seconds":null}}]`, "seconds must not be null", 0},
		{"trailing value", valid + " []", "trailing data", 0},
		{"trailing garbage", valid + " invalid", "trailing data", 0},
		{"null", "null", "rules are required", 0},
		{"empty", "[]", "", 0},
		{"invalid rule", `[{"mask":{"baseAsset":"*","quoteAsset":"USD"},"mode":"split","priceVariant":"normal","fills":1}]`, "invalid test execution fills", 0},
		{"valid rule", valid, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := decodeTestExecutionPolicy(tc.rules)
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("decode error = %v, want %q", err, tc.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if policy.Rules == nil || len(policy.Rules) != tc.wantRules {
				t.Fatalf("rules = %+v, want non-nil slice with %d rules", policy.Rules, tc.wantRules)
			}
		})
	}
}
