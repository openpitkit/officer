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

import { KeyRound } from "lucide-react";

import {
  registerLocaleResources,
  registerNav,
  registerPage,
  registerPolicy,
  registerRoute,
  registerRowAction,
  registerScope,
  registerWidget,
  unregisterNav,
  unregisterPage,
  unregisterPolicy,
  unregisterRoute,
  unregisterRowAction,
  unregisterScope,
  unregisterWidget,
} from "@openpit/officer-web";

import {
  HostReferencePage,
  HostReferenceWidget,
  ReplacementHostReferencePage,
  ReplacementHostReferenceWidget,
} from "./components";
import {
  hiddenActionID,
  hiddenPermission,
  hostActionID,
  hostNavID,
  hostPageID,
  hostPermission,
  hostPolicyID,
  hostScopeID,
  hostWidgetID,
  removedRouteID,
} from "./ids";

export function registerCustomHostComposition(): void {
  unregisterCustomHostComposition();
  registerLocaleResources("en", "customhost", {
    page: {
      title: "Host reference page",
      replacementTitle: "Replacement host reference page",
    },
    nav: { host: "Host reference" },
    widget: {
      title: "Host reference widget",
      replacementTitle: "Replacement host reference widget",
    },
  });
  registerScope(hostScopeID);
  registerPolicy({
    id: hostPolicyID,
    allowedScopes: [hostScopeID],
    kinds: [{ kind: "threshold" }],
    catalog: {
      wikiUrl: "https://openpit.dev/docs/examples/host-policy",
      fields: [{ key: "limit" }],
    },
  });

  registerRoute({
    id: hostPageID,
    path: "/host-reference",
    order: 1000,
    Component: HostReferencePage,
    permission: hostPermission,
  });
  registerPage({
    id: hostPageID,
    titleKey: "customhost:page.title",
    Component: HostReferencePage,
  });
  registerPage({
    id: hostPageID,
    titleKey: "customhost:page.title",
    Component: ReplacementHostReferencePage,
  });
  registerNav({
    id: hostNavID,
    to: "/host-reference",
    labelKey: "customhost:nav.host",
    icon: KeyRound,
    section: "primary",
    order: 1000,
    permission: hostPermission,
  });
  registerWidget({
    id: hostWidgetID,
    order: 1000,
    Component: HostReferenceWidget,
  });
  registerWidget({
    id: hostWidgetID,
    order: 1000,
    Component: ReplacementHostReferenceWidget,
  });
  registerRowAction<object, object>({
    id: hostActionID,
    kind: "customhost",
    order: 1000,
    permission: hostPermission,
    render: () => <button type="button" aria-label="customhost-action" />,
  });
  registerRowAction<object, object>({
    id: hiddenActionID,
    kind: "customhost",
    order: 1010,
    permission: hiddenPermission,
    render: () => <button type="button" aria-label="customhost-hidden" />,
  });
  registerRoute({
    id: removedRouteID,
    path: "/removed-host-reference",
    order: 1010,
    Component: HostReferencePage,
  });
  unregisterRoute(removedRouteID);
}

export function unregisterCustomHostComposition(): void {
  unregisterRoute(hostPageID);
  unregisterRoute(removedRouteID);
  unregisterNav(hostNavID);
  unregisterPage(hostPageID);
  unregisterWidget(hostWidgetID);
  unregisterRowAction(hostActionID);
  unregisterRowAction(hiddenActionID);
  unregisterPolicy(hostPolicyID);
  unregisterScope(hostScopeID);
}
