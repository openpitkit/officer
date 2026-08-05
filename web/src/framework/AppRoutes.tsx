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

import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { useTranslation } from "react-i18next";

import { useHasPermission } from "./auth/auth-context";
import { getPage } from "./registries/pages";
import { getRoutes } from "./registries/routes";

export function RedirectPreservingQuery({ to }: { to: string }) {
  const { search, hash } = useLocation();
  return <Navigate to={{ pathname: to, search, hash }} replace />;
}

function NotFoundRoute() {
  const { t } = useTranslation("errors");

  return (
    <main className="flex min-h-0 flex-1 items-center justify-center p-6">
      <h1 className="text-lg font-semibold text-text-muted">
        {t("code.not_found")}
      </h1>
    </main>
  );
}

export function AppRoutes() {
  const hasPermission = useHasPermission();

  return (
    <Routes>
      {getRoutes().map((entry) => {
        if (entry.permission && !hasPermission(entry.permission)) {
          return null;
        }
        let element = entry.element;
        if (entry.redirectTo) {
          element = <RedirectPreservingQuery to={entry.redirectTo} />;
        } else if (!element && entry.Component) {
          const override = getPage(entry.id);
          const Component =
            override &&
            (!override.permission || hasPermission(override.permission)) &&
            (override.when?.() ?? true)
              ? override.Component
              : entry.Component;
          element = <Component />;
        }
        return <Route key={entry.id} path={entry.path} element={element} />;
      })}
      <Route path="*" element={<NotFoundRoute />} />
    </Routes>
  );
}
