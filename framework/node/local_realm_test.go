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

package node

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/engine"
	"go.openpit.dev/officer/framework/store"
)

type configuredRealmStore struct {
	*memoryStore
	selected domain.RealmID
	bindings []domain.RealmID
}

func (st *configuredRealmStore) ForRealm(_ context.Context, realm domain.RealmID) (store.RealmStore, error) {
	st.bindings = append(st.bindings, realm)
	if realm != st.selected {
		return nil, domain.ErrInvalid
	}
	return st.realm, nil
}

func TestLocalNodeRetainsConfiguredRealmAcrossReset(t *testing.T) {
	for _, realm := range []domain.RealmID{"tenant-a", "tenant-β"} {
		t.Run(string(realm), func(t *testing.T) {
			ctx := context.Background()
			st := &configuredRealmStore{memoryStore: newMemoryStore("realm.db"), selected: realm}
			if err := st.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			build := func(snapshot engine.Snapshot) (engine.Engine, error) {
				built, err := fakeBuild(newFakeEngine(), new(engine.Snapshot))(snapshot)
				if err != nil {
					return nil, err
				}
				return lifecycleMarketDataEngine{Engine: built, owner: &lifecycleMarketDataServiceOwner{}}, nil
			}
			target, _, err := NewLocalNode(ctx, realm, st, build, failOnFatal(t))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := target.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := target.ResetDatabase(ctx, domain.Caller{Source: domain.SourceSystem}); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(st.bindings, []domain.RealmID{realm, realm}) {
				t.Fatalf("realm bindings = %v", st.bindings)
			}
		})
	}
}

func TestLocalNodeRejectsEmptyRealmBeforeBinding(t *testing.T) {
	st := &configuredRealmStore{memoryStore: newMemoryStore("realm.db"), selected: domain.DefaultRealm}
	_, _, err := NewLocalNode(
		context.Background(), "", st, fakeBuild(newFakeEngine(), new(engine.Snapshot)),
		failOnFatal(t),
	)
	if !errors.Is(err, domain.ErrInvalid) || len(st.bindings) != 0 {
		t.Fatalf("invalid realm: %v; bindings = %v", err, st.bindings)
	}
}
