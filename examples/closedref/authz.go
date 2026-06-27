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

package main

import (
	"context"

	"go.openpit.dev/officer/framework/domain"
)

type denyOneAuthorizer struct {
	routePermission string
	toolName        string
}

func newDenyOneAuthorizer(routePermission, toolName string) denyOneAuthorizer {
	return denyOneAuthorizer{routePermission: routePermission, toolName: toolName}
}

func (a denyOneAuthorizer) Authorize(
	_ context.Context,
	_ domain.Caller,
	permission string,
) error {
	if permission == a.routePermission || permission == a.toolName {
		return domain.ErrForbidden
	}
	return nil
}
