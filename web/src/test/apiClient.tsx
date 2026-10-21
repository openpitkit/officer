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

import {
  render,
  type RenderOptions,
  type RenderResult,
} from "@testing-library/react";
import type { ReactElement } from "react";
import { vi } from "vitest";

import type { AccountModeState } from "@/api/accountMode";
import type { Account } from "@/api/types";
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
): RenderResult {
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

/** A JSON response as the server sends it. */
export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

export type Route = (init: RequestInit) => Response;

// Answers each request from `routes`, keyed by method and path; any other
// request fails as unreachable.
export function serving(routes: Record<string, Route>) {
  return vi.fn<(url: string, init: RequestInit) => Promise<Response>>(
    async (url, init) => {
      const route = routes[`${init.method} ${url}`];
      if (route === undefined) {
        throw new TypeError(`unexpected ${init.method} ${url}`);
      }
      return route(init);
    },
  );
}

/**
 * A failure in the shared `{"error":{"code","message"}}` body, with `headers`
 * such as `Retry-After`. The code doubles as the message unless one is given.
 */
export function errorResponse(
  status: number,
  code: string,
  headers: Record<string, string> = {},
  message = code,
): Response {
  const res = jsonResponse({ error: { code, message } }, status);
  for (const [name, value] of Object.entries(headers)) {
    res.headers.set(name, value);
  }
  return res;
}

/** Answers GET /account-mode with `state` until a PUT stores `stored`, and
 *  with the stored state from then on, as the server does. */
export function answeringAccountMode(
  state: AccountModeState,
  stored?: AccountModeState,
) {
  let current = state;
  return vi.fn(async (_url: string, init: RequestInit) => {
    if (init.method !== "PUT") {
      return jsonResponse(current);
    }
    if (stored === undefined) {
      throw new Error("unexpected PUT /account-mode");
    }
    current = stored;
    return jsonResponse(stored);
  });
}

/** An unblocked account row with no group, titled after its code. */
export function accountRow(
  code: string,
  overrides: Partial<Account> = {},
): Account {
  return {
    code,
    title: code,
    blocked: false,
    blockReason: "",
    blockSource: "none",
    accountBlocked: false,
    accountBlockReason: "",
    groupBlocked: false,
    groupBlockReason: "",
    group: "",
    notes: "",
    pnlHaltReason: "",
    ...overrides,
  };
}
