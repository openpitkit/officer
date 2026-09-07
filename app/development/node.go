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

// Package development assembles a local node from Officer's SQLite store and
// native OpenPit engine for embedding in an external development process.
package development

import (
	"context"
	"errors"
	"fmt"

	"go.openpit.dev/officer/app/engine/native"
	"go.openpit.dev/officer/framework/domain"
	"go.openpit.dev/officer/framework/node"
	"go.openpit.dev/officer/internal/store/sqlite"
)

// Config supplies the dataset and the explicit local runtime resources.
type Config struct {
	Realm              domain.RealmID
	DatabasePath       string
	RuntimeLibraryPath string
}

// NewNode opens and initializes the SQLite store, hydrates the real engine,
// and transfers ownership of both to the returned node. Close releases them.
// Neither a database path nor a native runtime path is inferred.
func NewNode(ctx context.Context, cfg Config) (node.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := domain.ValidateRealmID(cfg.Realm); err != nil {
		return nil, err
	}
	if cfg.DatabasePath == "" || cfg.RuntimeLibraryPath == "" {
		return nil, errors.New("database path and native runtime library path are required")
	}
	st, err := sqlite.New(cfg.DatabasePath, sqlite.WithRealm(cfg.Realm))
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize node store: %w", err), st.Close())
	}
	target, _, err := node.NewLocalNode(
		ctx, st, native.NewOpenPitEngineBuildFunc(cfg.RuntimeLibraryPath), node.WithRealm(cfg.Realm),
	)
	if err != nil {
		return nil, errors.Join(err, st.Close())
	}
	return target, nil
}
