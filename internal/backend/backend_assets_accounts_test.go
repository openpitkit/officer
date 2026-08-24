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

package backend_test

import (
	"context"
	"errors"
	"testing"

	"go.openpit.dev/officer/framework/domain"
)

func TestService_CreateAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateAccount(ctx, domain.Account{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.createCalls) != 0 {
		t.Fatalf("invalid input must not reach the node")
	}

	if _, err := svc.CreateAccount(ctx, domain.Account{
		Code:  "acc-1",
		Title: "Account One",
	}); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if len(fn.createCalls) != 1 {
		t.Fatalf("valid create must route to node")
	}
	if fn.createCalls[0].Title != "Account One" {
		t.Fatalf("account title = %q", fn.createCalls[0].Title)
	}
}

func TestService_CreateAssetValidatesAndRoutes(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateAsset(ctx, domain.Asset{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty asset code, got %v", err)
	}
	if len(fn.createAssetCalls) != 0 {
		t.Fatalf("invalid input must not reach the node")
	}

	created, err := svc.CreateAsset(ctx, domain.Asset{
		Code:       "AAPL",
		Title:      "Apple Inc.",
		AssetClass: "equity",
	})
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	if created.Code != "AAPL" || created.Title != "Apple Inc." ||
		created.AssetClass != "equity" {
		t.Fatalf("created asset = %+v", created)
	}
	if len(fn.createAssetCalls) != 1 {
		t.Fatalf("valid create must route to node")
	}
}

func TestService_CreateAssetClassValidatesAndRoutes(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := context.Background()

	if _, err := svc.CreateAssetClass(ctx, domain.AssetClass{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty class code, got %v", err)
	}
	if len(fn.createAssetClassCalls) != 0 {
		t.Fatalf("invalid input must not reach the node")
	}

	created, err := svc.CreateAssetClass(ctx, domain.AssetClass{
		Code:  "equity",
		Title: "Equity",
		Notes: "listed shares",
	})
	if err != nil {
		t.Fatalf("CreateAssetClass: %v", err)
	}
	if created.Code != "equity" || created.Title != "Equity" || created.Notes != "listed shares" {
		t.Fatalf("created class = %+v", created)
	}
	if len(fn.createAssetClassCalls) != 1 {
		t.Fatalf("valid create must route to node")
	}
}

func TestService_UpdateAssetClassValidatesAndRoutes(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := context.Background()
	fn.assetClasses = []domain.AssetClass{{Code: "equity"}}

	if _, err := svc.UpdateAssetClass(ctx, "equity", domain.AssetClass{Code: ""}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty new code, got %v", err)
	}
	updated, err := svc.UpdateAssetClass(ctx, "equity", domain.AssetClass{Code: "stock", Title: "Stock"})
	if err != nil {
		t.Fatalf("UpdateAssetClass: %v", err)
	}
	if updated.Code != "stock" || updated.Title != "Stock" {
		t.Fatalf("updated class = %+v", updated)
	}
}

func TestService_UpdateAssetRenames(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := context.Background()
	fn.assets = []domain.Asset{{Code: "AAPL", Title: "Apple"}}

	if _, err := svc.UpdateAsset(ctx, "AAPL", domain.Asset{Code: ""}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty new code, got %v", err)
	}
	updated, err := svc.UpdateAsset(ctx, "AAPL", domain.Asset{Code: "AAPL.US", Title: "Apple Inc."})
	if err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	if updated.Code != "AAPL.US" || updated.Title != "Apple Inc." {
		t.Fatalf("updated asset = %+v", updated)
	}
	if fn.assets[0].Code != "AAPL.US" {
		t.Fatalf("node asset after rename = %+v", fn.assets[0])
	}
}

func TestService_BlockAccountValidates(t *testing.T) {
	t.Parallel()
	svc, fn := newTestService(t)
	ctx := context.Background()

	if err := svc.BlockAccount(ctx, "", "risk", domain.MissingAccountCreate); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for empty id, got %v", err)
	}
	if len(fn.blockCalls) != 0 {
		t.Fatalf("invalid id must not reach the node")
	}

	if err := svc.BlockAccount(ctx, "acc-1", "risk", domain.MissingAccountCreate); err != nil {
		t.Fatalf("BlockAccount: %v", err)
	}
	if len(fn.blockCalls) != 1 || !fn.blockCalls[0].blocked ||
		fn.blockCalls[0].reason != "risk" {
		t.Fatalf("block not routed correctly: %+v", fn.blockCalls)
	}
}
