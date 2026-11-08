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

package httpapi

import (
	"slices"
	"testing"

	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

func TestReferenceRoutesExcludeRuntimeAndListSurfaces(t *testing.T) {
	registry := &httpx.RouteRegistry{}
	RegisterReferenceRoutes(registry, nil)
	want := []string{
		"assets.create.post", "assets.update.put", "assets.delete.delete",
		"asset_classes.create.post", "asset_classes.update.put", "asset_classes.delete.delete",
		"accounts.create.post", "accounts.get", "accounts.update.put", "accounts.block.post",
		"accounts.unblock.post", "accounts.delete", "accounts.group.put", "accounts.currency.put", "accounts.notes.put",
		"groups.create.post", "groups.default.currency.put", "groups.get", "groups.update.put",
		"groups.currency.put", "groups.notes.put", "groups.block.post", "groups.unblock.post", "groups.delete",
	}
	var got []string
	for _, route := range registry.Routes() {
		got = append(got, route.ID)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("reference route surface = %v; want %v", got, want)
	}
	RegisterListRoutes(registry, nil)
	if len(registry.Routes()) != len(want)+11 {
		t.Fatal("reference registration changed existing list routes")
	}
}
