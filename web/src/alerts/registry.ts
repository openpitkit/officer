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

import type { TFunction } from "i18next";
import type { ComponentType } from "react";

export interface ActionableAlert {
  id: string;
  type: string;
  at: string;
  source: string;
  payload: unknown;
  read: boolean;
  actions: string[];
  deliveries: { channel: string; failed: boolean }[];
}

/** A concrete type owns its summary, payload rendering and decision controls.
 * The registered renderer must use the server-declared actions and call resolve
 * with its validated form values. Payloads render as React text, never HTML. */
export interface AlertRenderer {
  summary: (alert: ActionableAlert, t: TFunction) => string;
  Detail: ComponentType<{
    alert: ActionableAlert;
    pending: boolean;
    resolve: (action: string, input: unknown) => Promise<void>;
  }>;
}

const renderers = new Map<string, AlertRenderer>();

/** Register once at standalone composition time, alongside the business type. */
export function registerAlertRenderer(
  type: string,
  renderer: AlertRenderer,
): void {
  if (!type.trim() || renderers.has(type)) {
    throw new Error("Invalid or duplicate alert renderer registration");
  }
  renderers.set(type, renderer);
}

export function getAlertRenderer(type: string): AlertRenderer | undefined {
  return renderers.get(type);
}

/** Formats the same plain-text summary for the drawer and browser. */
export function alertSummary(alert: ActionableAlert, t: TFunction): string {
  const renderer = getAlertRenderer(alert.type);
  return renderer
    ? renderer.summary(alert, t)
    : t("alerts:unsupported", { type: alert.type });
}
