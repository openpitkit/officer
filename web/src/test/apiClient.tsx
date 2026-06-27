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

import { render, type RenderOptions } from "@testing-library/react";
import type { ReactElement } from "react";

import i18n from "@/i18n";
import {
  ApiClientProvider,
  createApiClient,
  createOfficerApi,
  type ApiClientConfig,
  type OfficerApi,
} from "@/framework";

type RenderWithApiOptions = Omit<RenderOptions, "wrapper"> & {
  api?: Partial<OfficerApi>;
  fetch?: typeof fetch;
  config?: Partial<ApiClientConfig>;
};

/** Render a component under the framework API provider. */
export function renderWithApi(
  ui: ReactElement,
  options: RenderWithApiOptions = {},
) {
  const { api, fetch, config, ...renderOptions } = options;
  const apiConfig: ApiClientConfig = {
    baseUrl: "/app/api/v1",
    fetch,
    translate: (key, params) => i18n.t(key, params),
    ...config,
  };
  return render(
    <ApiClientProvider config={apiConfig} api={api as OfficerApi | undefined}>
      {ui}
    </ApiClientProvider>,
    renderOptions,
  );
}

/** Build an endpoint surface over a mock fetch implementation. */
export function makeApi(fetchStub: typeof fetch) {
  return createOfficerApi(
    createApiClient({
      baseUrl: "/app/api/v1",
      fetch: fetchStub,
      translate: (key, params) => i18n.t(key, params),
    }),
  );
}
