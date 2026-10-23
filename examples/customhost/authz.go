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

package main

import (
	"context"

	"go.openpit.dev/officer/framework/domain"
	httpx "go.openpit.dev/officer/framework/web/httpapi"
)

type denyOneAuthorizer struct {
	routeID  string
	toolName string
	next     httpx.Authorizer
}

func newDenyOneAuthorizer(routeID, toolName string, next httpx.Authorizer) denyOneAuthorizer {
	return denyOneAuthorizer{routeID: routeID, toolName: toolName, next: next}
}

func (a denyOneAuthorizer) Authorize(
	ctx context.Context,
	caller domain.Caller,
	identifier string,
) error {
	if identifier == a.routeID || identifier == a.toolName {
		return domain.ErrForbidden
	}
	return a.next.Authorize(ctx, caller, identifier)
}
