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

// Approval-envelope decode + Ed25519 verify helpers used by the order
// reproduction / verification panel. These decode the artifacts CLIENT-SIDE for
// verification only; the signed bytes are extracted verbatim (never
// re-stringified) so a signature check always targets the exact bytes signed.

/** Decode a base64url string to a Uint8Array (no padding required). */
export function base64urlToBytes(s: string): Uint8Array {
  const b64 = s.replace(/-/g, "+").replace(/_/g, "/");
  const padded = b64 + "=".repeat((4 - (b64.length % 4)) % 4);
  const binary = atob(padded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/** Decode a standard base64 string (with padding) to a Uint8Array. */
export function base64StdToBytes(s: string): Uint8Array {
  const binary = atob(s);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

/** Extract the verbatim JSON value substring for "approval" from the envelope
 *  JSON string. The signature covers these exact bytes, so the substring is
 *  scanned out (never re-stringified) from the opening '{' to its matching '}',
 *  honoring string literals and backslash escapes. */
export function extractApprovalJsonSubstring(envJson: string): string {
  let keyIdx = -1;
  for (let from = 0; ; ) {
    const idx = envJson.indexOf('"approval"', from);
    if (idx === -1) {
      break;
    }
    const prev = envJson[idx - 1];
    if (prev === "{" || prev === ",") {
      keyIdx = idx;
      break;
    }
    from = idx + 1;
  }
  if (keyIdx === -1) {
    throw new Error("approval key not found in envelope");
  }
  let i = keyIdx + '"approval"'.length;
  while (i < envJson.length && envJson[i] !== "{") {
    i++;
  }
  if (i >= envJson.length) {
    throw new Error("approval object not found");
  }
  const start = i;
  let depth = 0;
  while (i < envJson.length) {
    const ch = envJson[i];
    if (ch === "{") {
      depth++;
      i++;
    } else if (ch === "}") {
      depth--;
      i++;
      if (depth === 0) {
        break;
      }
    } else if (ch === '"') {
      i++;
      while (i < envJson.length) {
        if (envJson[i] === "\\") {
          i += 2;
        } else if (envJson[i] === '"') {
          i++;
          break;
        } else {
          i++;
        }
      }
    } else {
      i++;
    }
  }
  return envJson.slice(start, i);
}

/** The engine result section a generalized (non-submit) payload may carry. */
export interface ParsedApprovalResult {
  outcome: string;
  fillQuantity: string;
  fillPrice: string;
  fillLockPrice: string;
  leavesQuantity: string;
  orderStatus: string;
}

/** The fields parsed out of an approval payload for the read-only breakdown.
 *  The payload is generalized across request types, so requestType and the
 *  optional engine result section may be present. */
export interface ParsedApproval {
  alg: string;
  keyId: string;
  requestType: string;
  side: string;
  quantity: string;
  amountKind: string;
  orderType: string;
  limitPrice: string;
  instrument: string;
  accountId: string;
  verdict: string;
  policySummary: string;
  estimatePrice: string;
  issuedAt: string;
  result: ParsedApprovalResult | null;
}

function str(v: unknown): string {
  return typeof v === "string" ? v : "";
}

function parseResult(v: unknown): ParsedApprovalResult | null {
  if (typeof v !== "object" || v === null) {
    return null;
  }
  const r = v as Record<string, unknown>;
  return {
    outcome: str(r.outcome),
    fillQuantity: str(r.fillQuantity),
    fillPrice: str(r.fillPrice),
    fillLockPrice: str(r.fillLockPrice),
    leavesQuantity: str(r.leavesQuantity),
    orderStatus: str(r.orderStatus),
  };
}

/** Parse the approval payload object into the read-only breakdown fields. Reads
 *  only; never used as the signed bytes. Gracefully handles the generalized
 *  payload: requestType and a result section may or may not be present. */
export function parseApprovalFields(
  approval: Record<string, unknown>,
): ParsedApproval {
  return {
    alg: str(approval.alg),
    keyId: str(approval.keyId),
    requestType: str(approval.requestType),
    side: str(approval.side),
    quantity: str(approval.quantity),
    amountKind: str(approval.amountKind),
    orderType: str(approval.orderType),
    limitPrice: str(approval.limitPrice),
    instrument: str(approval.instrument),
    accountId: str(approval.accountId),
    verdict: str(approval.verdict),
    policySummary: str(approval.policySummary),
    estimatePrice: str(approval.estimatePrice),
    issuedAt: str(approval.issuedAt),
    result: parseResult(approval.result),
  };
}

/** Parse the approval fields from a persisted approval token. Returns null when
 *  the token cannot be decoded as a signed approval envelope. */
export function parseTokenApproval(token: string): ParsedApproval | null {
  try {
    const envJson = new TextDecoder().decode(base64urlToBytes(token));
    const env = JSON.parse(envJson) as { approval?: Record<string, unknown> };
    if (!env.approval || typeof env.approval !== "object") {
      return null;
    }
    return parseApprovalFields(env.approval);
  } catch {
    return null;
  }
}

/** Verify an Ed25519 signature over canonicalBytes with a raw-base64 public key
 *  (32 raw bytes, base64-encoded). Throws on malformed key or signature. */
export async function verifyEd25519(
  canonicalBytes: Uint8Array,
  signatureBase64: string,
  rawKeyBase64: string,
): Promise<boolean> {
  const sig = base64StdToBytes(signatureBase64);
  const keyBytes = base64StdToBytes(rawKeyBase64);
  if (keyBytes.length !== 32) {
    throw new Error("public key is not a 32-byte Ed25519 key");
  }
  const cryptoKey = await crypto.subtle.importKey(
    "raw",
    keyBytes as BufferSource,
    { name: "Ed25519" },
    false,
    ["verify"],
  );
  return crypto.subtle.verify(
    { name: "Ed25519" },
    cryptoKey,
    sig as BufferSource,
    canonicalBytes as BufferSource,
  );
}
