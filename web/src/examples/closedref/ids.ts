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

export const privatePageID = "closedref.private.page";
export const privateNavID = "closedref.private.nav";
export const privateWidgetID = "closedref.private.widget";
export const privateActionID = "closedref.private.action";
export const hiddenActionID = "closedref.hidden.action";
export const removedRouteID = "closedref.removed.route";
export const privateScopeID = "closedref_scope";
export const privatePolicyID = "closedref_policy";
export const privatePermission: Permission = "closedref.private";
export const hiddenPermission: Permission = "closedref.hidden";
