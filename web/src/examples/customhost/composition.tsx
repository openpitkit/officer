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
  PrivateReferencePage,
  PrivateReferenceWidget,
  ReplacementPrivateReferencePage,
  ReplacementPrivateReferenceWidget,
} from "./components";
import {
  hiddenActionID,
  hiddenPermission,
  privateActionID,
  privateNavID,
  privatePageID,
  privatePermission,
  privatePolicyID,
  privateScopeID,
  privateWidgetID,
  removedRouteID,
} from "./ids";

export function registerCustomHostComposition(): void {
  unregisterCustomHostComposition();
  registerLocaleResources("en", "customhost", {
    page: {
      title: "Private reference page",
      replacementTitle: "Replacement private reference page",
    },
    nav: { private: "Private reference" },
    widget: {
      title: "Private reference widget",
      replacementTitle: "Replacement private reference widget",
    },
  });
  registerScope(privateScopeID);
  registerPolicy({
    id: privatePolicyID,
    allowedScopes: [privateScopeID],
    kinds: [{ kind: "threshold" }],
    catalog: {
      wikiUrl: "https://openpit.dev/docs/examples/private-policy",
      fields: [{ key: "limit" }],
    },
  });

  registerRoute({
    id: privatePageID,
    path: "/private-reference",
    order: 1000,
    Component: PrivateReferencePage,
    permission: privatePermission,
  });
  registerPage({
    id: privatePageID,
    titleKey: "customhost:page.title",
    Component: PrivateReferencePage,
  });
  registerPage({
    id: privatePageID,
    titleKey: "customhost:page.title",
    Component: ReplacementPrivateReferencePage,
  });
  registerNav({
    id: privateNavID,
    to: "/private-reference",
    labelKey: "customhost:nav.private",
    icon: KeyRound,
    section: "primary",
    order: 1000,
    permission: privatePermission,
  });
  registerWidget({
    id: privateWidgetID,
    order: 1000,
    Component: PrivateReferenceWidget,
  });
  registerWidget({
    id: privateWidgetID,
    order: 1000,
    Component: ReplacementPrivateReferenceWidget,
  });
  registerRowAction<object, object>({
    id: privateActionID,
    kind: "customhost",
    order: 1000,
    permission: privatePermission,
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
    path: "/removed-private-reference",
    order: 1010,
    Component: PrivateReferencePage,
  });
  unregisterRoute(removedRouteID);
}

export function unregisterCustomHostComposition(): void {
  unregisterRoute(privatePageID);
  unregisterRoute(removedRouteID);
  unregisterNav(privateNavID);
  unregisterPage(privatePageID);
  unregisterWidget(privateWidgetID);
  unregisterRowAction(privateActionID);
  unregisterRowAction(hiddenActionID);
  unregisterPolicy(privatePolicyID);
  unregisterScope(privateScopeID);
}
