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

import { type Permission } from "@openpit/officer-web";

export const hostPageID = "customhost.host.page";
export const hostNavID = "customhost.host.nav";
export const hostWidgetID = "customhost.host.widget";
export const hostActionID = "customhost.host.action";
export const hiddenActionID = "customhost.hidden.action";
export const removedRouteID = "customhost.removed.route";
export const hostScopeID = "customhost_scope";
export const hostPolicyID = "customhost_policy";
export const hostPermission: Permission = "customhost.host";
export const hiddenPermission: Permission = "customhost.hidden";
