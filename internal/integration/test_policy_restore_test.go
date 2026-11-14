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

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"go.openpit.dev/officer/engine"
	"go.openpit.dev/officer/framework/auth"
	"go.openpit.dev/officer/framework/backup"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/framework/store"
	"go.openpit.dev/officer/internal/store/sqlite"
)

type policyRestoreAuditStore struct {
	store.Store
	cause      error
	applied    *domain.TestExecutionPolicyState
	connection domain.ExternalID
}

func (s *policyRestoreAuditStore) ForRealm(ctx context.Context, realm domain.RealmID) (store.RealmStore, error) {
	r, err := s.Store.ForRealm(ctx, realm)
	if err != nil {
		return nil, err
	}
	return &policyRestoreAuditRealm{RealmStore: r, owner: s}, nil
}

type policyRestoreAuditRealm struct {
	store.RealmStore
	owner *policyRestoreAuditStore
}

func (r *policyRestoreAuditRealm) AppendAudit(ctx context.Context, entry store.AuditEntry) error {
	if entry.Action == domain.AuditActionRestoreBackup {
		state, err := r.GetTestExecutionPolicy(ctx, r.owner.connection)
		if err != nil {
			return err
		}
		r.owner.applied = &state
		return r.owner.cause
	}
	return r.RealmStore.AppendAudit(ctx, entry)
}

func TestNodeFailedRestoreKeepsTestPolicy(t *testing.T) {
	ctx := systemCtx()
	st, err := sqlite.New(filepath.Join(t.TempDir(), "policy-restore.db"), domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.BindExecutionPolicyProviders(func(provider string) (bool, error) {
		if provider == "test-fixture" {
			return true, nil
		}
		return false, domain.ErrTradingProviderUnavailable
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	realm, err := st.ForRealm(ctx, domain.DefaultRealm)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := realm.CreateTradingConnection(ctx, domain.TradingConnection{
		Provider: "test-fixture", Label: "keep-policy", VenueIdentity: "keep-policy",
		Mode: domain.TradingModePaper, Credentials: "{}",
	})
	if err != nil {
		t.Fatal(err)
	}
	mask, err := domain.ParseInstrumentMask("*", "USD")
	if err != nil {
		t.Fatal(err)
	}
	fills := 2
	_, want, err := realm.ReplaceTestExecutionPolicy(ctx, connection.ExternalID, domain.TestExecutionPolicy{
		Rules: []domain.TestExecutionRule{{Mask: mask, Mode: "split", PriceVariant: "best", Fills: &fills}},
	})
	if err != nil {
		t.Fatal(err)
	}
	scope := backup.Scope{Sections: []backup.Section{backup.SectionTrading}}
	archive, err := realm.ExportBackup(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	// A trading-only manifest keeps the node on its non-runtime rollback path.
	archive.Manifest.Sections = []backup.Section{backup.SectionTrading}
	archive.Data.TestExecutionPolicies = []backup.TestExecutionPolicy{{
		Connection: connection.ExternalID, Revision: 73, Rules: json.RawMessage("[]"),
	}}
	cause := errors.New("restore audit refused")
	wrapped := &policyRestoreAuditStore{Store: st, cause: cause, connection: connection.ExternalID}
	target, _, err := node.NewLocalNode(ctx, domain.DefaultRealm, wrapped,
		engine.NewOpenPitEngineBuildFunc(), func(err error) { t.Errorf("unexpected fatal: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close() })
	if _, _, err := target.RestoreBackup(ctx, archive, backup.RestoreOptions{
		Scope: scope, Mode: backup.RestoreModeOverwrite,
	}, auth.SystemCaller()); !errors.Is(err, cause) {
		t.Fatalf("restore error = %v, want audit failure", err)
	}
	if wrapped.applied == nil || wrapped.applied.Revision != 73 || len(wrapped.applied.Policy.Rules) != 0 {
		t.Fatalf("forward restore was not applied before failure: %+v", wrapped.applied)
	}
	got, err := realm.GetTestExecutionPolicy(ctx, connection.ExternalID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("policy after failed restore = %+v, %v; want %+v", got, err, want)
	}
}
