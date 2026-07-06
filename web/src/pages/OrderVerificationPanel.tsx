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

import { useEffect, useState } from "react";
import type { ReactElement, ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { KeyRound, ShieldCheck } from "lucide-react";

import { ApiError, useOfficerApi } from "@/framework";
import type {
  EventReproduction,
  EventReproductionResponse,
  PublicKeyMaterial,
} from "@/api/types";
import { formatDateTime } from "@/i18n/format";
import {
  base64urlToBytes,
  extractApprovalJsonSubstring,
  parseApprovalFields,
  parseTokenApproval,
  verifyEd25519,
  type ParsedApproval,
} from "@/lib/approvalEnvelope";
import { cn } from "@/lib/utils";
import { Badge, type BadgeProps } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { CopyableSnippet } from "@/components/CopyableSnippet";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Textarea } from "@/components/ui/textarea";

// ---------------------------------------------------------------------------
// Panel state + verify UI. Envelope decode / Ed25519 verify live in
// @/lib/approvalEnvelope. The signed artifacts are shown verbatim from the
// server strings; nothing decoded here is presented as the signed payload.
// ---------------------------------------------------------------------------

type VerifyState =
  | { phase: "idle" }
  | { phase: "verifying" }
  | { phase: "valid" }
  | { phase: "invalid" }
  | { phase: "unsigned" }
  | { phase: "error"; message: string };

// The four request types Officer attests. An unknown value degrades to the
// verbatim string so a new backend type never renders as blank.
const REQUEST_TYPE_KEYS: Record<string, string> = {
  submit: "requestType.submit",
  execution_report: "requestType.executionReport",
  confirm: "requestType.confirm",
  cancel: "requestType.cancel",
};

function requestTypeLabel(
  t: (key: string) => string,
  requestType: string,
): string {
  const key = REQUEST_TYPE_KEYS[requestType];
  return key ? t(key) : requestType;
}

// ---------------------------------------------------------------------------
// Ledger primitives
// ---------------------------------------------------------------------------

/** A tracked-uppercase micro-label over a hairline-ruled artifact block. */
function LedgerBlock({
  label,
  note,
  children,
}: {
  label: string;
  note?: ReactNode;
  children: ReactNode;
}): ReactElement {
  return (
    <section className="space-y-2 border-t border-border pt-3">
      <div className="space-y-0.5">
        <h4 className="text-[0.625rem] font-medium uppercase tracking-[0.09em] text-muted">
          {label}
        </h4>
        {note ? (
          <p className="text-[0.625rem] leading-snug text-muted-lt">{note}</p>
        ) : null}
      </div>
      {children}
    </section>
  );
}

/** One label/value line in the read-only breakdown. */
function BreakdownRow({
  label,
  value,
}: {
  label: string;
  value: string;
}): ReactElement | null {
  if (!value) {
    return null;
  }
  return (
    <div className="flex items-baseline justify-between gap-4 py-1">
      <span className="text-[0.625rem] uppercase tracking-[0.08em] text-muted-lt">
        {label}
      </span>
      <span className="nums text-right font-mono text-[0.6875rem] text-text">
        {value}
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Verify snippets (JS / Go), with actual artifacts substituted
// ---------------------------------------------------------------------------

function jsVerifySnippet(
  canonical: string,
  signatureBase64: string,
  rawKeyBase64: string,
): string {
  return [
    "// WebCrypto Ed25519 verification of the reproduced canonical bytes.",
    `const canonical = ${JSON.stringify(canonical)};`,
    `const signatureB64 = ${JSON.stringify(signatureBase64)};`,
    `const publicKeyRawB64 = ${JSON.stringify(rawKeyBase64)};`,
    "const b64 = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));",
    "const key = await crypto.subtle.importKey(",
    '  "raw", b64(publicKeyRawB64), { name: "Ed25519" }, false, ["verify"],',
    ");",
    "const ok = await crypto.subtle.verify(",
    '  { name: "Ed25519" }, key, b64(signatureB64),',
    "  new TextEncoder().encode(canonical),",
    ");",
    "console.log(ok);",
  ].join("\n");
}

function goVerifySnippet(
  canonical: string,
  signatureBase64: string,
  rawKeyBase64: string,
): string {
  return [
    "// crypto/ed25519 verification of the reproduced canonical bytes.",
    "package main",
    "",
    "import (",
    '\t"crypto/ed25519"',
    '\t"encoding/base64"',
    '\t"fmt"',
    ")",
    "",
    "func main() {",
    `\tcanonical := []byte(${goQuote(canonical)})`,
    `\tsig, _ := base64.StdEncoding.DecodeString(${goQuote(signatureBase64)})`,
    `\tpub, _ := base64.StdEncoding.DecodeString(${goQuote(rawKeyBase64)})`,
    "\tok := ed25519.Verify(ed25519.PublicKey(pub), canonical, sig)",
    '\tfmt.Println(ok)',
    "}",
  ].join("\n");
}

/** Quote a string as a Go double-quoted literal. */
function goQuote(s: string): string {
  const escaped = s
    .replace(/\\/g, "\\\\")
    .replace(/"/g, '\\"')
    .replace(/\n/g, "\\n")
    .replace(/\t/g, "\\t")
    .replace(/\r/g, "\\r");
  return `"${escaped}"`;
}

// ---------------------------------------------------------------------------
// Verdict badge + verify controls (shared by both modes)
// ---------------------------------------------------------------------------

function verifyBadge(
  t: (key: string) => string,
  state: VerifyState,
): ReactElement {
  const map: Record<
    VerifyState["phase"],
    { variant: BadgeProps["variant"]; key: string }
  > = {
    idle: { variant: "neutral", key: "badgeUnverified" },
    verifying: { variant: "neutral", key: "badgeUnverified" },
    valid: { variant: "ok", key: "badgeValid" },
    invalid: { variant: "danger", key: "badgeInvalid" },
    unsigned: { variant: "neutral", key: "badgeUnsigned" },
    error: { variant: "danger", key: "badgeError" },
  };
  const entry = map[state.phase];
  return (
    <Badge variant={entry.variant}>{t(`verify.${entry.key}`)}</Badge>
  );
}

function VerifyResultLine({
  state,
}: {
  state: VerifyState;
}): ReactElement | null {
  const { t } = useTranslation("orders");
  if (state.phase === "valid") {
    return (
      <p className="text-[0.6875rem] text-[var(--ok)]">
        {t("verify.validNote")}
      </p>
    );
  }
  if (state.phase === "invalid") {
    return (
      <p className="text-[0.6875rem] text-[var(--danger)]">
        {t("verify.invalidNote")}
      </p>
    );
  }
  if (state.phase === "unsigned") {
    return (
      <p className="text-[0.6875rem] text-muted-lt">
        {t("verify.unsignedNote")}
      </p>
    );
  }
  if (state.phase === "error") {
    return (
      <p className="text-[0.6875rem] text-[var(--danger)]">
        {t("verify.errorNote", { message: state.message })}
      </p>
    );
  }
  return null;
}

/** Read-only, clearly-labeled human breakdown of a parsed approval. */
function ApprovalBreakdown({
  fields,
}: {
  fields: ParsedApproval;
}): ReactElement {
  const { t } = useTranslation("orders");
  const issued = fields.issuedAt ? formatDateTime(fields.issuedAt) : "";
  const expires = fields.expiresAt ? formatDateTime(fields.expiresAt) : "";
  const requestType = fields.requestType
    ? requestTypeLabel(t, fields.requestType)
    : "";
  return (
    <LedgerBlock
      label={t("breakdown.label")}
      note={t("breakdown.readOnlyNote")}
    >
      <div className="divide-y divide-border rounded-card border border-border bg-surface-2 px-3">
        <BreakdownRow
          label={t("breakdown.requestType")}
          value={requestType}
        />
        <BreakdownRow label={t("breakdown.verdict")} value={fields.verdict} />
        <BreakdownRow label={t("breakdown.side")} value={fields.side} />
        <BreakdownRow
          label={t("breakdown.quantity")}
          value={fields.quantity}
        />
        <BreakdownRow
          label={t("breakdown.amountKind")}
          value={fields.amountKind}
        />
        <BreakdownRow
          label={t("breakdown.orderType")}
          value={fields.orderType}
        />
        <BreakdownRow
          label={t("breakdown.limitPrice")}
          value={fields.limitPrice}
        />
        <BreakdownRow
          label={t("breakdown.estimatePrice")}
          value={fields.estimatePrice}
        />
        <BreakdownRow
          label={t("breakdown.instrument")}
          value={fields.instrument}
        />
        <BreakdownRow
          label={t("breakdown.account")}
          value={fields.accountId}
        />
        <BreakdownRow
          label={t("breakdown.policySummary")}
          value={fields.policySummary}
        />
        {fields.result ? (
          <>
            <BreakdownRow
              label={t("breakdown.outcome")}
              value={fields.result.outcome}
            />
            <BreakdownRow
              label={t("breakdown.fillQuantity")}
              value={fields.result.fillQuantity}
            />
            <BreakdownRow
              label={t("breakdown.fillPrice")}
              value={fields.result.fillPrice}
            />
            <BreakdownRow
              label={t("breakdown.leavesQuantity")}
              value={fields.result.leavesQuantity}
            />
            <BreakdownRow
              label={t("breakdown.orderStatus")}
              value={fields.result.orderStatus}
            />
          </>
        ) : null}
        <BreakdownRow label={t("breakdown.issuedAt")} value={issued} />
        <BreakdownRow label={t("breakdown.expiresAt")} value={expires} />
      </div>
    </LedgerBlock>
  );
}

// ---------------------------------------------------------------------------
// Mode A — reproduction of one event's artifacts
// ---------------------------------------------------------------------------

type ReproState =
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "ready"; bundle: EventReproduction };

/** The single response facet present for a request type, plus its verbatim token
 *  and the localized label describing the facet. */
function presentResponseFacet(
  response: EventReproductionResponse | null,
): { labelKey: string; token: string; body: Record<string, unknown> } | null {
  if (!response) {
    return null;
  }
  if (response.submitResponse) {
    return {
      labelKey: "repro.responseSubmit",
      token: response.submitResponse.token,
      body: { submitResponse: response.submitResponse },
    };
  }
  if (response.executionReport) {
    return {
      labelKey: "repro.responseExecutionReport",
      token: response.executionReport.attestationToken,
      body: { executionReport: response.executionReport },
    };
  }
  if (response.confirm) {
    return {
      labelKey: "repro.responseConfirm",
      token: response.confirm.attestationToken,
      body: { confirm: response.confirm },
    };
  }
  if (response.cancel) {
    return {
      labelKey: "repro.responseCancel",
      token: response.cancel.attestationToken,
      body: { cancel: response.cancel },
    };
  }
  return null;
}

function ReproductionMode({
  orderExternalId,
  eventId,
}: {
  orderExternalId: string;
  eventId: string;
}): ReactElement {
  const { t } = useTranslation("orders");
  const { fetchEventReproduction, fetchPublicKeyById } = useOfficerApi();

  const [state, setState] = useState<ReproState>({ phase: "loading" });
  const [verifyState, setVerifyState] = useState<VerifyState>({ phase: "idle" });
  // The attestation's public key in raw-base64 form, resolved by its keyId.
  // WebCrypto import and the ready-to-run snippets need raw bytes; the bundle
  // only carries pem-pkcs8. Null until fetched or when the event is unsigned.
  const [rawKey, setRawKey] = useState<string | null>(null);

  useEffect(() => {
    // Reset to the loading state before the fetch starts; intentional.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setState({ phase: "loading" });
    setVerifyState({ phase: "idle" });
    setRawKey(null);
    const controller = new AbortController();
    fetchEventReproduction(orderExternalId, eventId, controller.signal)
      .then((bundle) => {
        if (controller.signal.aborted) {
          return;
        }
        setState({ phase: "ready", bundle });
        const keyId =
          bundle.attestation?.keyId || bundle.publicKey?.keyId || "";
        if (bundle.eSign.alg === "ed25519" && keyId) {
          fetchPublicKeyById(keyId, "raw-base64", controller.signal)
            .then((material) => {
              if (!controller.signal.aborted) {
                setRawKey(material.key);
              }
            })
            .catch(() => {
              // A missing raw key only disables the copyable snippets and the
              // live verify falls back to its own fetch; the bundle stays shown.
            });
        }
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ phase: "error", message: errMessage(err) });
        }
      });
    return () => controller.abort();
  }, [fetchEventReproduction, fetchPublicKeyById, orderExternalId, eventId]);

  if (state.phase === "loading") {
    return (
      <p className="py-6 text-center text-xs text-muted-lt">
        {t("repro.loading")}
      </p>
    );
  }
  if (state.phase === "error") {
    return (
      <p className="rounded-card border border-[var(--danger)] bg-[var(--danger-dim)] px-3 py-2 text-xs text-[var(--danger)]">
        {t("repro.loadError", { message: state.message })}
      </p>
    );
  }

  const { bundle } = state;
  const attestation = bundle.attestation;

  async function runVerify() {
    if (state.phase !== "ready") {
      return;
    }
    const b = state.bundle;
    if (b.eSign.alg === "none" || !b.attestation) {
      setVerifyState({ phase: "unsigned" });
      return;
    }
    if (!b.canonicalApproval || !b.signature) {
      setVerifyState({
        phase: "error",
        message: t("verify.missingArtifacts"),
      });
      return;
    }
    setVerifyState({ phase: "verifying" });
    try {
      // Verify against the ATTESTATION's keyId public key (rotation-safe), in raw
      // form for WebCrypto import. Reuse the key already fetched on load, else
      // resolve it by the attestation's keyId now.
      const keyId = b.attestation.keyId || b.publicKey?.keyId || "";
      const key =
        rawKey ?? (await fetchPublicKeyById(keyId, "raw-base64")).key;
      const canonical = new TextEncoder().encode(b.canonicalApproval);
      const ok = await verifyEd25519(canonical, b.signature, key);
      setVerifyState(ok ? { phase: "valid" } : { phase: "invalid" });
    } catch (err) {
      setVerifyState({ phase: "error", message: errMessage(err) });
    }
  }

  const requestType = requestTypeLabel(t, bundle.requestType);

  // No attestation envelope: show the reason and a clean empty state.
  if (!attestation) {
    return (
      <div className="space-y-3">
        <RequestTypeHeader requestType={requestType} />
        <EsignStatus
          alg={bundle.eSign.alg}
          signed={bundle.eSign.signed}
          noESign={bundle.eSign.noESign}
        />
        <p className="rounded-card border border-border bg-surface-2 px-3 py-4 text-center text-xs text-muted-lt">
          {bundle.reason || t("repro.noAttestation")}
        </p>
        <LedgerBlock
          label={t("repro.eventBodyLabel")}
          note={t("repro.eventBodyNote")}
        >
          <CopyableSnippet text={jsonText(bundle.event)} rows={8} />
        </LedgerBlock>
      </div>
    );
  }

  const unsigned = bundle.eSign.alg === "none";
  const parsed = parseTokenApproval(attestation.token);
  const facet = presentResponseFacet(bundle.response);

  return (
    <div className="space-y-3">
      <RequestTypeHeader requestType={requestType} />
      <EsignStatus
        alg={bundle.eSign.alg}
        signed={bundle.eSign.signed}
        noESign={bundle.eSign.noESign}
      />

      <LedgerBlock label={t("repro.tokenLabel")} note={t("repro.tokenNote")}>
        <CopyableSnippet text={attestation.token} rows={3} />
      </LedgerBlock>

      {bundle.request ? (
        <LedgerBlock
          label={t("repro.requestLabel")}
          note={t("repro.requestNote")}
        >
          <CopyableSnippet text={jsonText(bundle.request)} rows={8} />
        </LedgerBlock>
      ) : null}

      {facet ? (
        <LedgerBlock
          label={t(facet.labelKey)}
          note={t("repro.responseNote")}
        >
          {facet.token ? (
            <div className="mb-2">
              <CopyableSnippet
                label={t("repro.responseTokenLabel")}
                text={facet.token}
                rows={3}
              />
            </div>
          ) : null}
          <CopyableSnippet text={jsonText(facet.body)} rows={8} />
        </LedgerBlock>
      ) : null}

      {bundle.canonicalApproval ? (
        <LedgerBlock
          label={t("repro.canonicalLabel")}
          note={t("repro.canonicalNote")}
        >
          <CopyableSnippet text={bundle.canonicalApproval} rows={5} />
        </LedgerBlock>
      ) : null}

      {!unsigned && bundle.publicKey ? (
        <LedgerBlock
          label={t("repro.signatureLabel")}
          note={t("repro.signatureNote")}
        >
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <KeyRound className="h-3.5 w-3.5 text-accent" />
            <span className="text-[0.625rem] uppercase tracking-[0.08em] text-muted-lt">
              {t("repro.keyId")}
            </span>
            <span className="nums break-all font-mono text-[0.6875rem] text-accent">
              {bundle.publicKey.keyId}
            </span>
          </div>
          <CopyableSnippet
            label={t("repro.signatureFieldLabel")}
            text={bundle.signature}
            rows={2}
          />
          <div className="mt-2">
            <CopyableSnippet
              label={t("repro.publicKeyFieldLabel", {
                format: bundle.publicKey.format,
              })}
              text={bundle.publicKey.key}
              rows={4}
            />
          </div>
        </LedgerBlock>
      ) : null}

      <LedgerBlock label={t("verify.label")} note={t("verify.note")}>
        <div className="flex flex-wrap items-center gap-3">
          {verifyBadge(t, verifyState)}
          {!unsigned ? (
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                void runVerify();
              }}
              disabled={verifyState.phase === "verifying"}
            >
              <ShieldCheck className="h-3.5 w-3.5" />
              {verifyState.phase === "verifying"
                ? t("verify.verifying")
                : t("verify.button")}
            </Button>
          ) : (
            <span className="text-[0.6875rem] text-muted-lt">
              {t("verify.unsignedNote")}
            </span>
          )}
        </div>
        <div className="mt-2">
          <VerifyResultLine state={verifyState} />
        </div>

        {!unsigned && bundle.canonicalApproval && rawKey ? (
          <div className="mt-3 space-y-2">
            <CopyableSnippet
              label={t("verify.jsSnippetLabel")}
              text={jsVerifySnippet(
                bundle.canonicalApproval,
                bundle.signature,
                rawKey,
              )}
              rows={8}
            />
            <CopyableSnippet
              label={t("verify.goSnippetLabel")}
              text={goVerifySnippet(
                bundle.canonicalApproval,
                bundle.signature,
                rawKey,
              )}
              rows={9}
            />
            <p className="text-[0.625rem] text-muted-lt">
              {t("verify.snippetKeyNote")}
            </p>
          </div>
        ) : null}
      </LedgerBlock>

      {parsed ? <ApprovalBreakdown fields={parsed} /> : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Mode B — paste any token and verify it
// ---------------------------------------------------------------------------

interface PasteResult {
  parsed: ParsedApproval;
  canonical: string;
  keyId: string;
  expired: boolean;
}

function VerifyTokenMode(): ReactElement {
  const { t } = useTranslation("orders");
  const { fetchPublicKeyById } = useOfficerApi();

  const [token, setToken] = useState("");
  const [verifyState, setVerifyState] = useState<VerifyState>({ phase: "idle" });
  const [result, setResult] = useState<PasteResult | null>(null);

  async function runVerify() {
    setResult(null);
    const trimmed = token.trim();
    if (!trimmed) {
      setVerifyState({ phase: "error", message: t("verify.pasteEmpty") });
      return;
    }
    setVerifyState({ phase: "verifying" });
    try {
      const envJson = new TextDecoder().decode(base64urlToBytes(trimmed));
      const env = JSON.parse(envJson) as {
        alg?: string;
        signature?: string;
        keyId?: string;
        approval?: Record<string, unknown>;
      };
      if (!env.approval || typeof env.approval !== "object") {
        setVerifyState({ phase: "error", message: t("verify.pasteMalformed") });
        return;
      }
      const fields = parseApprovalFields(env.approval);
      // The signed bytes are the EXACT approval substring, never a re-stringify.
      const canonical = extractApprovalJsonSubstring(envJson);
      const keyId = fields.keyId || env.keyId || "";
      const now = Date.now();
      const expiresMs = fields.expiresAt ? Date.parse(fields.expiresAt) : NaN;
      const expired = Number.isFinite(expiresMs) && expiresMs < now;

      const alg = fields.alg || env.alg || "";
      if (alg === "none") {
        setResult({ parsed: fields, canonical, keyId, expired });
        setVerifyState({ phase: "unsigned" });
        return;
      }
      if (!keyId) {
        setVerifyState({ phase: "error", message: t("verify.pasteNoKeyId") });
        return;
      }
      let material: PublicKeyMaterial;
      try {
        material = await fetchPublicKeyById(keyId, "raw-base64");
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) {
          setVerifyState({
            phase: "error",
            message: t("verify.pasteUnknownKey", { keyId }),
          });
          setResult({ parsed: fields, canonical, keyId, expired });
          return;
        }
        throw err;
      }
      const ok = await verifyEd25519(
        new TextEncoder().encode(canonical),
        env.signature ?? "",
        material.key,
      );
      setResult({ parsed: fields, canonical, keyId, expired });
      setVerifyState(ok ? { phase: "valid" } : { phase: "invalid" });
    } catch (err) {
      setVerifyState({ phase: "error", message: errMessage(err) });
    }
  }

  return (
    <div className="space-y-3">
      <LedgerBlock label={t("paste.label")} note={t("paste.note")}>
        <Textarea
          value={token}
          spellCheck={false}
          rows={4}
          onChange={(e) => setToken(e.target.value)}
          placeholder={t("paste.placeholder")}
          aria-label={t("paste.label")}
          className="resize-none font-mono text-[0.6875rem]"
        />
        <div className="mt-2 flex flex-wrap items-center gap-3">
          <Button
            size="sm"
            onClick={() => {
              void runVerify();
            }}
            disabled={verifyState.phase === "verifying"}
          >
            <ShieldCheck className="h-3.5 w-3.5" />
            {verifyState.phase === "verifying"
              ? t("verify.verifying")
              : t("verify.button")}
          </Button>
          {verifyBadge(t, verifyState)}
          {result?.expired ? (
            <Badge variant="warn">{t("verify.expired")}</Badge>
          ) : null}
        </div>
        <div className="mt-2">
          <VerifyResultLine state={verifyState} />
        </div>
      </LedgerBlock>

      {result ? (
        <>
          {result.keyId ? (
            <div className="flex flex-wrap items-center gap-2">
              <KeyRound className="h-3.5 w-3.5 text-accent" />
              <span className="text-[0.625rem] uppercase tracking-[0.08em] text-muted-lt">
                {t("repro.keyId")}
              </span>
              <span className="nums break-all font-mono text-[0.6875rem] text-accent">
                {result.keyId}
              </span>
            </div>
          ) : null}
          <ApprovalBreakdown fields={result.parsed} />
        </>
      ) : null}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Request-type header + eSign status strip
// ---------------------------------------------------------------------------

function RequestTypeHeader({
  requestType,
}: {
  requestType: string;
}): ReactElement | null {
  const { t } = useTranslation("orders");
  if (!requestType) {
    return null;
  }
  return (
    <div className="flex flex-wrap items-center gap-2">
      <h4 className="text-[0.625rem] font-medium uppercase tracking-[0.09em] text-muted">
        {t("repro.requestTypeLabel")}
      </h4>
      <Badge variant="neutral">{requestType}</Badge>
    </div>
  );
}

function EsignStatus({
  alg,
  signed,
  noESign,
}: {
  alg: string;
  signed: boolean;
  noESign: boolean;
}): ReactElement {
  const { t } = useTranslation("orders");
  const badge = signed ? (
    <Badge variant="ok">{t("esign.signed")}</Badge>
  ) : (
    <Badge variant="neutral">{t("esign.unsigned")}</Badge>
  );
  return (
    <div className="flex flex-wrap items-center gap-3 border-t border-border pt-3">
      <h4 className="text-[0.625rem] font-medium uppercase tracking-[0.09em] text-muted">
        {t("esign.label")}
      </h4>
      {badge}
      <span className="text-[0.6875rem] text-muted-lt">
        {alg ? t("esign.alg", { alg }) : t("esign.noAlg")}
        {noESign ? ` · ${t("esign.offGlobal")}` : ""}
      </span>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Verbatim JSON reconstruction (server serializer produced these objects; we
// stringify the whole DTO, which is allowed — only the SIGNED artifacts must
// stay byte-verbatim, and those are copied as their own server strings above).
// ---------------------------------------------------------------------------

function jsonText(value: unknown): string {
  return JSON.stringify(value, null, 2);
}

// ---------------------------------------------------------------------------
// Helpers shared across modes
// ---------------------------------------------------------------------------

function errMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return err.message;
  }
  return err instanceof Error ? err.message : String(err);
}

// ---------------------------------------------------------------------------
// The panel dialog
// ---------------------------------------------------------------------------

export type PanelMode = "reproduction" | "verify";

/** Two-mode controller panel: reproduce one order EVENT's exact API artifacts
 *  and verify them, or paste any token and verify it standalone. Reproduction
 *  keys on the (order, event) pair since Officer signs every engine-processed
 *  request 1:1 with the event it produced. */
export function OrderVerificationPanel({
  orderExternalId,
  eventId = null,
  initialMode = "reproduction",
  open,
  onOpenChange,
}: {
  /** The order whose event to reproduce, or null to open in paste-to-verify. */
  orderExternalId: string | null;
  /** The event to reproduce; null for paste-to-verify only. */
  eventId?: string | null;
  initialMode?: PanelMode;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}): ReactElement {
  const { t } = useTranslation("orders");
  const hasEvent = orderExternalId !== null && eventId !== null;
  const [mode, setMode] = useState<PanelMode>(
    hasEvent ? initialMode : "verify",
  );

  // Re-seed the mode each time the panel opens so a reopened panel starts on the
  // requested tab rather than the last-viewed one.
  useEffect(() => {
    if (open) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setMode(hasEvent ? initialMode : "verify");
    }
  }, [open, hasEvent, initialMode]);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle className="font-display text-lg">
            {t("panel.title")}
          </DialogTitle>
          <DialogDescription>{t("panel.description")}</DialogDescription>
        </DialogHeader>

        <div
          className="flex w-fit gap-1 rounded-card border border-border bg-surface-2 p-1"
          role="tablist"
          aria-label={t("panel.title")}
        >
          {hasEvent ? (
            <ModeTab
              active={mode === "reproduction"}
              label={t("panel.tabReproduction")}
              onClick={() => setMode("reproduction")}
            />
          ) : null}
          <ModeTab
            active={mode === "verify"}
            label={t("panel.tabVerify")}
            onClick={() => setMode("verify")}
          />
        </div>

        <div className="max-h-[62vh] overflow-y-auto pr-1">
          {mode === "reproduction" && hasEvent ? (
            <ReproductionMode
              orderExternalId={orderExternalId}
              eventId={eventId}
            />
          ) : (
            <VerifyTokenMode />
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function ModeTab({
  active,
  label,
  onClick,
}: {
  active: boolean;
  label: string;
  onClick: () => void;
}): ReactElement {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={cn(
        "rounded-badge px-3 py-1 text-xs font-medium transition-colors duration-[180ms]",
        active
          ? "bg-accent-dim text-accent"
          : "text-muted-lt hover:bg-surface-hover hover:text-text",
      )}
    >
      {label}
    </button>
  );
}
