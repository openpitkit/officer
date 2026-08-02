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

import type { ApiErrorDependent } from "../../api/types";

/** Stable codes the backend maps from its domain sentinels. */
export type ApiErrorCode =
  | "validation"
  | "not_found"
  | "has_dependents"
  | "conflict"
  | "terminal_order"
  | "execution_report_required"
  | "precondition"
  | "too_large"
  | "engine_restarting"
  | "not_implemented"
  | "account_missing"
  | "internal"
  | "network"
  | "signing";

/** A failed API call, carrying the HTTP status and decoded error code. */
export class ApiError extends Error {
  readonly status?: number;
  readonly code: ApiErrorCode;
  readonly dependents?: ApiErrorDependent[];
  /** The account code the request named, when `code` is "account_missing". */
  readonly account?: string;

  constructor(
    message: string,
    code: ApiErrorCode,
    status?: number,
    dependents?: ApiErrorDependent[],
    account?: string,
  ) {
    super(message);
    this.name = "ApiError";
    this.code = code;
    this.status = status;
    this.dependents = dependents;
    this.account = account;
  }
}

/** Host-supplied transport and localization configuration. */
export interface ApiClientConfig {
  /** Base URL prefix for every endpoint. */
  baseUrl: string;
  /** Transport used for every request. Defaults to globalThis.fetch. */
  fetch?: typeof fetch;
  /** Static headers merged into every request. */
  headers?: Record<string, string>;
  /** Per-request headers, awaited on each call. */
  getHeaders?: () => Record<string, string> | Promise<Record<string, string>>;
  /** Error-message localization. Defaults to English framework fallbacks. */
  translate?: (key: string, params?: Record<string, unknown>) => string;
}

/** JSON request options shared by all endpoint methods. */
export interface RequestOptions {
  method?: string;
  body?: unknown;
  signal?: AbortSignal;
}

/** Injectable Pit Officer API transport. */
export interface ApiClient {
  readonly baseUrl: string;
  request(path: string, opts?: RequestOptions): Promise<unknown>;
  requestBlob(
    path: string,
    opts: {
      method?: string;
      body?: unknown;
      signal?: AbortSignal;
      accept: string;
      fallbackFilename: string;
    },
  ): Promise<{ blob: Blob; filename: string }>;
}

type Json = Record<string, unknown>;

function isObject(v: unknown): v is Json {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

function pick(obj: Json, ...keys: string[]): unknown {
  for (const key of keys) {
    if (key in obj) {
      return obj[key];
    }
  }
  return undefined;
}

function asString(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function asInt(v: unknown): number {
  return typeof v === "number" && Number.isFinite(v) ? v : 0;
}

function asCode(v: unknown): ApiErrorCode {
  switch (v) {
    case "validation":
    case "not_found":
    case "has_dependents":
    case "conflict":
    case "terminal_order":
    case "execution_report_required":
    case "precondition":
    case "too_large":
    case "engine_restarting":
    case "not_implemented":
    case "account_missing":
    case "internal":
    case "signing":
      return v;
    default:
      return "internal";
  }
}

function defaultTranslate(
  key: string,
  params?: Record<string, unknown>,
): string {
  const codeMessages: Record<string, string> = {
    validation: "The request was rejected as invalid.",
    not_found: "The requested resource was not found.",
    has_dependents: "The resource has dependent rows.",
    conflict: "The request conflicts with the current state.",
    terminal_order: "The order is in a terminal status.",
    execution_report_required:
      "This order already has execution-report activity. Submit a complete execution report through the order workflow.",
    precondition: "A precondition for the request was not met.",
    too_large:
      "The import exceeds the maximum size of 128 MiB. Split the export into smaller files or use the API for bulk loading.",
    engine_restarting:
      "The risk engine is restarting; retry after it finishes.",
    not_implemented: "The requested operation is not implemented.",
    account_missing: "The account does not exist.",
    internal: "The service encountered an internal error.",
    network: "Could not reach the service.",
    signing: "The signing operation was rejected.",
  };
  if (key.startsWith("errors:code.")) {
    return (
      codeMessages[key.slice("errors:code.".length)] ?? codeMessages.internal
    );
  }
  if (key === "errors:http") {
    return `request to ${String(params?.path ?? "")} failed with HTTP ${String(
      params?.status ?? "",
    )}`;
  }
  if (key === "errors:network") {
    return `network error contacting ${String(params?.path ?? "")}: ${String(
      params?.detail ?? "",
    )}`;
  }
  if (key === "errors:invalidJson") {
    return `response from ${String(params?.path ?? "")} was not valid JSON`;
  }
  return key;
}

function defaultMessage(
  code: ApiErrorCode,
  translate: ApiClientConfig["translate"],
): string {
  return (translate ?? defaultTranslate)(`errors:code.${code}`);
}

function clientOwnedMessage(code: ApiErrorCode): boolean {
  return (
    code === "terminal_order" ||
    code === "execution_report_required" ||
    code === "too_large"
  );
}

function asDependents(v: unknown): ApiErrorDependent[] | undefined {
  if (!Array.isArray(v)) return undefined;
  return v
    .filter(isObject)
    .map((o) => ({
      kind: asString(pick(o, "kind", "Kind")),
      count: asInt(pick(o, "count", "Count")),
    }))
    .filter((dep) => dep.kind.length > 0 && dep.count > 0);
}

function asAccountCode(v: unknown): string | undefined {
  const s = asString(v);
  return s.length > 0 ? s : undefined;
}

async function toApiError(
  res: Response,
  requestPath: string,
  translate: ApiClientConfig["translate"],
): Promise<ApiError> {
  let code = asCode(undefined);
  let bodyMessage = "";
  let dependents: ApiErrorDependent[] | undefined;
  let account: string | undefined;
  try {
    const body = (await res.json()) as unknown;
    if (isObject(body)) {
      const err = pick(body, "error", "Error");
      if (isObject(err)) {
        code = asCode(pick(err, "code", "Code"));
        bodyMessage = asString(pick(err, "message", "Message"));
        dependents = asDependents(pick(err, "dependents", "Dependents"));
        account = asAccountCode(pick(err, "account", "Account"));
      }
    }
  } catch {
    /* No JSON body; infer the code from the status and use a localized message. */
  }
  if (code === "internal") {
    if (res.status === 400) {
      code = "validation";
    } else if (res.status === 404) {
      code = "not_found";
    } else if (res.status === 409) {
      code = "conflict";
    } else if (res.status === 422) {
      code = "precondition";
    } else if (res.status === 413) {
      code = "too_large";
    } else if (res.status === 503) {
      code = "engine_restarting";
    } else if (res.status === 501) {
      code = "not_implemented";
    }
  }
  const t = translate ?? defaultTranslate;
  const message = clientOwnedMessage(code)
    ? defaultMessage(code, t)
    : bodyMessage.length > 0
      ? bodyMessage
      : code === "internal"
        ? t("errors:http", { path: requestPath, status: res.status })
        : defaultMessage(code, t);
  return new ApiError(message, code, res.status, dependents, account);
}

function filenameFromDisposition(value: string | null): string {
  if (!value) {
    return "";
  }
  const encoded = /(?:^|;)\s*filename\*=UTF-8''([^;]+)/i.exec(value);
  if (encoded) {
    try {
      return decodeURIComponent(encoded[1]);
    } catch {
      return encoded[1];
    }
  }
  const quoted = /(?:^|;)\s*filename="([^"]+)"/i.exec(value);
  if (quoted) {
    return quoted[1];
  }
  const bare = /(?:^|;)\s*filename=([^;]+)/i.exec(value);
  return bare ? bare[1].trim() : "";
}

function fetchFor(config: ApiClientConfig): typeof fetch {
  return config.fetch ?? globalThis.fetch.bind(globalThis);
}

async function requestHeaders(
  config: ApiClientConfig,
  accept: string,
  body: unknown,
): Promise<Record<string, string>> {
  return {
    Accept: accept,
    ...(body !== undefined ? { "Content-Type": "application/json" } : {}),
    ...config.headers,
    ...((await config.getHeaders?.()) ?? {}),
  };
}

/** Create an API client using host-provided transport configuration. */
export function createApiClient(config: ApiClientConfig): ApiClient {
  const translate = config.translate ?? defaultTranslate;

  async function request(
    requestPath: string,
    opts: RequestOptions = {},
  ): Promise<unknown> {
    const { method = "GET", body, signal } = opts;
    const headers = await requestHeaders(config, "application/json", body);
    const payload = body === undefined ? undefined : JSON.stringify(body);

    let res: Response;
    try {
      res = await fetchFor(config)(requestPath, {
        method,
        headers,
        body: payload,
        signal,
      });
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        throw err;
      }
      throw new ApiError(
        translate("errors:network", {
          path: requestPath,
          detail: err instanceof Error ? err.message : String(err),
        }),
        "network",
      );
    }

    if (!res.ok) {
      throw await toApiError(res, requestPath, translate);
    }
    if (res.status === 204) {
      return undefined;
    }

    try {
      return (await res.json()) as unknown;
    } catch {
      throw new ApiError(
        translate("errors:invalidJson", { path: requestPath }),
        "internal",
        res.status,
      );
    }
  }

  async function requestBlob(
    requestPath: string,
    opts: {
      method?: string;
      body?: unknown;
      signal?: AbortSignal;
      accept: string;
      fallbackFilename: string;
    },
  ): Promise<{ blob: Blob; filename: string }> {
    const { method = "GET", body, signal, accept, fallbackFilename } = opts;
    const headers = await requestHeaders(config, accept, body);
    const payload = body === undefined ? undefined : JSON.stringify(body);

    let res: Response;
    try {
      res = await fetchFor(config)(requestPath, {
        method,
        headers,
        body: payload,
        signal,
      });
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        throw err;
      }
      throw new ApiError(
        translate("errors:network", {
          path: requestPath,
          detail: err instanceof Error ? err.message : String(err),
        }),
        "network",
      );
    }

    if (!res.ok) {
      throw await toApiError(res, requestPath, translate);
    }
    const filename = filenameFromDisposition(
      res.headers.get("Content-Disposition"),
    );
    const blob = await res.blob();
    return { blob, filename: filename || fallbackFilename };
  }

  return {
    baseUrl: config.baseUrl,
    request,
    requestBlob,
  };
}
