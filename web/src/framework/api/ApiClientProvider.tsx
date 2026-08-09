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

import {
  createContext,
  useContext,
  useMemo,
  type JSX,
  type ReactNode,
} from "react";

import {
  createApiClient,
  type ApiClient,
  type ApiClientConfig,
} from "./createApiClient";
import { createOfficerApi, type OfficerApi } from "./officerApi";

const ApiClientContext = createContext<ApiClient | null>(null);
const OfficerApiContext = createContext<OfficerApi | null>(null);

/** Provide an injected Pit Officer API client to framework consumers. */
export function ApiClientProvider(props: {
  config: ApiClientConfig;
  api?: OfficerApi;
  children: ReactNode;
}): JSX.Element {
  const client = useMemo(() => createApiClient(props.config), [props.config]);
  const api = useMemo(
    () => props.api ?? createOfficerApi(client),
    [client, props.api],
  );

  return (
    <ApiClientContext.Provider value={client}>
      <OfficerApiContext.Provider value={api}>
        {props.children}
      </OfficerApiContext.Provider>
    </ApiClientContext.Provider>
  );
}

/** Return the injected API transport. */
// eslint-disable-next-line react-refresh/only-export-components
export function useApiClient(): ApiClient {
  const client = useContext(ApiClientContext);
  if (!client) {
    throw new Error("useApiClient must be used inside ApiClientProvider");
  }
  return client;
}

/** Return the injected Pit Officer endpoint surface. */
// eslint-disable-next-line react-refresh/only-export-components
export function useOfficerApi(): OfficerApi {
  const api = useContext(OfficerApiContext);
  if (!api) {
    throw new Error("useOfficerApi must be used inside ApiClientProvider");
  }
  return api;
}
