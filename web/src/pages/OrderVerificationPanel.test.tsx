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

import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/framework";
import type {
  EventReproduction,
  OrderEvent,
  PublicKeyMaterial,
} from "@/api/types";
import i18n from "@/i18n";
import { OrderVerificationPanel } from "@/pages/OrderVerificationPanel";
import { renderWithApi } from "@/test/apiClient";

// A minimal but complete event body for the reproduction bundle.
const sampleEvent: OrderEvent = {
  externalId: "evt-repro-1",
  order: "ord-repro-1",
  at: "2026-06-24T00:00:00Z",
  type: "submitted",
  source: "api",
  signed: true,
  alg: "ed25519",
};

// --- base64 helpers matching the panel's own decoders --------------------

function bytesToBase64Std(bytes: Uint8Array): string {
  let s = "";
  for (const b of bytes) {
    s += String.fromCharCode(b);
  }
  return btoa(s);
}

function bytesToBase64Url(bytes: Uint8Array): string {
  return bytesToBase64Std(bytes)
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");
}

// A signed envelope with the exact bytes recoverable from the token, plus the
// raw-base64 public key and canonical bytes the panel receives from the server.
interface SignedFixture {
  token: string;
  canonical: string;
  signature: string;
  rawPublicKey: string;
  keyId: string;
}

// buildSignedToken produces a real Ed25519-signed envelope. The canonical bytes
// are JSON.stringify(approval) (the wire form the Go server marshals), the
// envelope embeds those exact bytes as its "approval" value, and the signature
// covers them — so the panel's substring extractor recovers the signed bytes
// byte-for-byte, exactly as production does.
async function buildSignedToken(
  keyId: string,
  overrides: Record<string, unknown> = {},
): Promise<SignedFixture> {
  const pair = (await crypto.subtle.generateKey({ name: "Ed25519" }, true, [
    "sign",
    "verify",
  ])) as CryptoKeyPair;
  const rawPub = new Uint8Array(
    await crypto.subtle.exportKey("raw", pair.publicKey),
  );

  const approval = {
    version: 1,
    mode: "immediate",
    requestType: "submit",
    orderExternalId: "ord-repro-1",
    instrument: "AAPL/USD",
    side: "buy",
    quantity: "10",
    amountKind: "quantity",
    orderType: "limit",
    limitPrice: "150.25",
    accountId: "acc-1",
    verdict: "accept",
    policySummary: "accepted",
    estimatePrice: "150.25",
    issuedAt: "2026-06-24T00:00:00Z",
    keyId,
    alg: "ed25519",
    ...overrides,
  };
  const canonical = JSON.stringify(approval);
  const sigBytes = new Uint8Array(
    await crypto.subtle.sign(
      { name: "Ed25519" },
      pair.privateKey,
      new TextEncoder().encode(canonical),
    ),
  );
  const signature = bytesToBase64Std(sigBytes);
  // Build the envelope by string concatenation so the "approval" value is the
  // EXACT canonical bytes (a JSON.stringify of the envelope would preserve them
  // too, but concatenation makes the byte-identity explicit for this test).
  const envJson = `{"approval":${canonical},"signature":${JSON.stringify(
    signature,
  )},"keyId":${JSON.stringify(keyId)},"alg":"ed25519"}`;
  const token = bytesToBase64Url(new TextEncoder().encode(envJson));
  return {
    token,
    canonical,
    signature,
    rawPublicKey: bytesToBase64Std(rawPub),
    keyId,
  };
}

// A submit-request reproduction bundle keyed to the fixture's signed envelope.
function submitBundleFrom(fixture: SignedFixture): EventReproduction {
  return {
    requestType: "submit",
    event: sampleEvent,
    attestation: {
      token: fixture.token,
      keyId: fixture.keyId,
      alg: "ed25519",
      requestType: "submit",
      mode: "immediate",
      issuedAt: "2026-06-24T00:00:00Z",
      signed: true,
    },
    request: {
      requestType: "submit",
      orderExternalId: "ord-repro-1",
      eventExternalId: "evt-repro-1",
      instrument: "AAPL/USD",
      side: "buy",
      quantity: "10",
      amountKind: "quantity",
      orderType: "limit",
      limitPrice: "150.25",
      priceCurrency: "USD",
      accountId: "acc-1",
      verdict: "accept",
      result: null,
    },
    response: {
      submitResponse: {
        token: fixture.token,
        keyId: fixture.keyId,
        orderExternalId: "ord-repro-1",
        verdict: "accept",
        reasons: [],
      },
      executionReport: null,
      confirm: null,
      cancel: null,
    },
    canonicalApproval: fixture.canonical,
    publicKey: {
      keyId: fixture.keyId,
      alg: "ed25519",
      format: "pem-pkcs8",
      key: "PEM-PLACEHOLDER",
    },
    eSign: { alg: "ed25519", noESign: false, signed: true },
    signature: fixture.signature,
    reason: "",
  };
}

// An execution-report reproduction bundle for a fill event: exercises a
// non-submit request type end-to-end (its own facet, token, and result).
function execReportBundleFrom(fixture: SignedFixture): EventReproduction {
  const fillEvent: OrderEvent = {
    externalId: "evt-fill-1",
    order: "ord-repro-1",
    at: "2026-06-24T00:01:00Z",
    type: "fill",
    source: "api",
    signed: true,
    alg: "ed25519",
  };
  return {
    requestType: "execution_report",
    event: fillEvent,
    attestation: {
      token: fixture.token,
      keyId: fixture.keyId,
      alg: "ed25519",
      requestType: "execution_report",
      mode: "immediate",
      issuedAt: "2026-06-24T00:01:00Z",
      signed: true,
    },
    request: {
      requestType: "execution_report",
      orderExternalId: "ord-repro-1",
      eventExternalId: "evt-fill-1",
      instrument: "AAPL/USD",
      side: "buy",
      quantity: "10",
      amountKind: "quantity",
      orderType: "limit",
      limitPrice: "150.25",
      priceCurrency: "USD",
      accountId: "acc-1",
      verdict: "accept",
      result: {
        outcome: "filled",
        fillQuantity: "10",
        fillPrice: "150.25",
        fillLockPrice: "150.25",
        leavesQuantity: "0",
        orderStatus: "filled",
        blocks: [],
      },
    },
    response: {
      submitResponse: null,
      executionReport: {
        blocks: [],
        outcomes: [],
        attestationToken: fixture.token,
        attestationKeyId: fixture.keyId,
        signed: true,
      },
      confirm: null,
      cancel: null,
    },
    canonicalApproval: fixture.canonical,
    publicKey: {
      keyId: fixture.keyId,
      alg: "ed25519",
      format: "pem-pkcs8",
      key: "PEM-PLACEHOLDER",
    },
    eSign: { alg: "ed25519", noESign: false, signed: true },
    signature: fixture.signature,
    reason: "",
  };
}

const fetchEventReproductionMock = vi.fn();
const fetchPublicKeyByIdMock = vi.fn();

function renderPanel(
  orderExternalId: string | null,
  eventId: string | null = "evt-repro-1",
  initialMode: "reproduction" | "verify" = "reproduction",
) {
  return renderWithApi(
    <I18nextProvider i18n={i18n}>
      <OrderVerificationPanel
        orderExternalId={orderExternalId}
        eventId={eventId}
        initialMode={initialMode}
        open
        onOpenChange={() => {}}
      />
    </I18nextProvider>,
    {
      api: {
        fetchEventReproduction: fetchEventReproductionMock,
        fetchPublicKeyById: fetchPublicKeyByIdMock,
      },
    },
  );
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
});

afterEach(() => {
  vi.useRealTimers();
});

// Find a readonly artifact textarea by its exact value (the LedgerBlock heading
// is the visual label, so artifacts are located by their verbatim content).
function textareaByValue(value: string): HTMLTextAreaElement | undefined {
  return screen
    .getAllByRole("textbox")
    .find((el) => (el as HTMLTextAreaElement).value === value) as
    | HTMLTextAreaElement
    | undefined;
}

describe("OrderVerificationPanel — reproduction mode", () => {
  it("renders the token and canonical bytes verbatim for a submit event", async () => {
    const fixture = await buildSignedToken("key-1");
    const bundle = submitBundleFrom(fixture);
    fetchEventReproductionMock.mockResolvedValue(bundle);
    fetchPublicKeyByIdMock.mockResolvedValue({
      keyId: "key-1",
      alg: "ed25519",
      format: "raw-base64",
      key: fixture.rawPublicKey,
    } satisfies PublicKeyMaterial);

    renderPanel("ord-repro-1", "evt-repro-1");

    await waitFor(() =>
      expect(fetchEventReproductionMock).toHaveBeenCalled(),
    );
    // The endpoint is keyed on the (order, event) pair.
    const [orderId, eventId] = fetchEventReproductionMock.mock.calls[0];
    expect(orderId).toBe("ord-repro-1");
    expect(eventId).toBe("evt-repro-1");

    // The token textarea holds the exact server token, byte-for-byte.
    await screen.findByText("Token");
    expect(textareaByValue(fixture.token)).toBeDefined();

    // The canonical block holds the exact signed bytes.
    expect(
      screen.getByText("Canonical form (signed bytes)"),
    ).toBeInTheDocument();
    expect(textareaByValue(fixture.canonical)).toBeDefined();

    // The rotation-safe key id is shown prominently.
    expect(screen.getAllByText("key-1").length).toBeGreaterThan(0);

    // The request-type header names the localized request type (it also
    // reappears in the read-only breakdown row, so both occurrences are present).
    expect(screen.getAllByText("Submit").length).toBeGreaterThan(0);
  });

  it("renders an execution-report event's facet, verbatim token, and result", async () => {
    const fixture = await buildSignedToken("key-er", {
      requestType: "execution_report",
    });
    const bundle = execReportBundleFrom(fixture);
    fetchEventReproductionMock.mockResolvedValue(bundle);
    fetchPublicKeyByIdMock.mockResolvedValue({
      keyId: "key-er",
      alg: "ed25519",
      format: "raw-base64",
      key: fixture.rawPublicKey,
    } satisfies PublicKeyMaterial);

    renderPanel("ord-repro-1", "evt-fill-1");

    // The execution-report response facet is shown, with its verbatim token.
    await screen.findByText("Execution-report response");
    expect(screen.getAllByText("Execution report").length).toBeGreaterThan(0);
    expect(textareaByValue(fixture.token)).toBeDefined();
    expect(textareaByValue(fixture.canonical)).toBeDefined();
  });

  it("verifies a valid signature against the attestation key id", async () => {
    const fixture = await buildSignedToken("key-1");
    fetchEventReproductionMock.mockResolvedValue(submitBundleFrom(fixture));
    fetchPublicKeyByIdMock.mockResolvedValue({
      keyId: "key-1",
      alg: "ed25519",
      format: "raw-base64",
      key: fixture.rawPublicKey,
    } satisfies PublicKeyMaterial);

    const user = userEvent.setup();
    renderPanel("ord-repro-1", "evt-repro-1");

    await screen.findByText("Token");
    await user.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() =>
      expect(screen.getByText("VALID")).toBeInTheDocument(),
    );
    // Verification resolved the ATTESTATION's key id, in raw-base64 (the on-load
    // fetch may carry an AbortSignal as a third arg).
    expect(fetchPublicKeyByIdMock).toHaveBeenCalled();
    const [keyId, format] = fetchPublicKeyByIdMock.mock.calls[0];
    expect(keyId).toBe("key-1");
    expect(format).toBe("raw-base64");
  });

  it("reports an invalid signature when the bytes do not match", async () => {
    const fixture = await buildSignedToken("key-1");
    const bundle = submitBundleFrom(fixture);
    // Corrupt the signature so verification fails.
    bundle.signature = bytesToBase64Std(new Uint8Array(64));
    fetchEventReproductionMock.mockResolvedValue(bundle);
    fetchPublicKeyByIdMock.mockResolvedValue({
      keyId: "key-1",
      alg: "ed25519",
      format: "raw-base64",
      key: fixture.rawPublicKey,
    } satisfies PublicKeyMaterial);

    const user = userEvent.setup();
    renderPanel("ord-repro-1", "evt-repro-1");

    await screen.findByText("Token");
    await user.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() =>
      expect(screen.getByText("INVALID")).toBeInTheDocument(),
    );
  });

  it("shows an unsigned state under eSign-off with no verify button", async () => {
    const fixture = await buildSignedToken("key-none", { alg: "none" });
    const bundle = submitBundleFrom(fixture);
    bundle.attestation = { ...bundle.attestation!, alg: "none", signed: false };
    bundle.eSign = { alg: "none", noESign: true, signed: false };
    bundle.signature = "";
    bundle.publicKey = null;
    fetchEventReproductionMock.mockResolvedValue(bundle);

    renderPanel("ord-repro-1", "evt-repro-1");

    // Token is still shown; no signature/public-key block, no verify button.
    await screen.findByText("Token");
    expect(screen.getAllByText("UNSIGNED").length).toBeGreaterThan(0);
    expect(
      screen.queryByRole("button", { name: "Verify" }),
    ).not.toBeInTheDocument();
    expect(fetchPublicKeyByIdMock).not.toHaveBeenCalled();
  });

  it("shows the reason and event body when there is no attestation", async () => {
    fetchEventReproductionMock.mockResolvedValue({
      requestType: "",
      event: sampleEvent,
      attestation: null,
      request: null,
      response: null,
      canonicalApproval: null,
      publicKey: null,
      eSign: { alg: "", noESign: false, signed: false },
      signature: "",
      reason: "event has no persisted attestation envelope",
    } satisfies EventReproduction);

    renderPanel("ord-repro-1", "evt-repro-1");

    expect(
      await screen.findByText("event has no persisted attestation envelope"),
    ).toBeInTheDocument();
    expect(screen.getByText("Event body")).toBeInTheDocument();
  });
});

describe("OrderVerificationPanel — verify token mode", () => {
  it("verifies a valid pasted token (happy path)", async () => {
    const fixture = await buildSignedToken("key-paste");
    fetchPublicKeyByIdMock.mockResolvedValue({
      keyId: "key-paste",
      alg: "ed25519",
      format: "raw-base64",
      key: fixture.rawPublicKey,
    } satisfies PublicKeyMaterial);

    const user = userEvent.setup();
    renderPanel(null, null, "verify");

    const area = await screen.findByLabelText("Token", {
      selector: "textarea",
    });
    await user.click(area);
    await user.paste(fixture.token);
    await user.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() =>
      expect(screen.getByText("VALID")).toBeInTheDocument(),
    );
    expect(fetchPublicKeyByIdMock).toHaveBeenCalledWith(
      "key-paste",
      "raw-base64",
    );
    // The read-only breakdown appears with the parsed fields.
    expect(screen.getByText("Human-readable breakdown")).toBeInTheDocument();
  });

  it("parses a generalized execution-report payload with its result section", async () => {
    const fixture = await buildSignedToken("key-gen", {
      approvalId: "approval-exec-1",
      approvalRef: "approval-submit-1",
      requestType: "execution_report",
      mode: "hold",
      eventExternalId: "evt-fill-1",
      venue: "XNAS",
      priceCurrency: "USD",
      timeInForce: "GTC",
      accountGroupId: "equity-desks",
      estimateSource: "limit",
      nonce: "nonce-exec-1",
      rejectCode: "policy_reject",
      rejectScope: "account",
      rejectPolicy: "daily_loss",
      rejectReason: "Daily loss threshold reached",
      principal: "operator@example.test",
      executionReport: {
        baseAsset: "AAPL",
        quoteAsset: "USD",
        fillQuantity: "10",
        fillPrice: "150.25",
        leavesQuantity: "0",
        lockPrice: "149.75",
        lock: "opaque-lock",
        commission: { amount: "-0.25", currency: "USD" },
        order: "ord-repro-1",
        account: "desk-alpha",
        side: "buy",
        orderStatus: "filled",
        force: false,
      },
      result: {
        outcome: "filled",
        fillQuantity: "10",
        fillPrice: "150.25",
        fillLockPrice: "149.75",
        commission: { amount: "-0.30", currency: "USD" },
        leavesQuantity: "0",
        orderStatus: "filled",
        blocks: [
          {
            account: "desk-alpha",
            code: "daily_loss",
            reason: "Daily loss threshold reached",
            details: "limit=-1000",
          },
        ],
      },
    });
    fetchPublicKeyByIdMock.mockResolvedValue({
      keyId: "key-gen",
      alg: "ed25519",
      format: "raw-base64",
      key: fixture.rawPublicKey,
    } satisfies PublicKeyMaterial);

    const user = userEvent.setup();
    renderPanel(null, null, "verify");

    const area = await screen.findByLabelText("Token", {
      selector: "textarea",
    });
    await user.click(area);
    await user.paste(fixture.token);
    await user.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() =>
      expect(screen.getByText("VALID")).toBeInTheDocument(),
    );
    // The result section is surfaced in the breakdown for a non-submit payload.
    expect(screen.getAllByText("Order status")).toHaveLength(2);
    expect(screen.getByText("Execution-report request")).toBeInTheDocument();
    expect(screen.getByText("Version")).toBeInTheDocument();
    expect(screen.getByText("approval-exec-1")).toBeInTheDocument();
    expect(screen.getByText("approval-submit-1")).toBeInTheDocument();
    expect(screen.getAllByText("ord-repro-1").length).toBeGreaterThan(0);
    expect(screen.getByText("evt-fill-1")).toBeInTheDocument();
    expect(screen.getByText("XNAS")).toBeInTheDocument();
    expect(screen.getByText("GTC")).toBeInTheDocument();
    expect(screen.getByText("equity-desks")).toBeInTheDocument();
    expect(screen.getAllByText("limit").length).toBeGreaterThan(0);
    expect(screen.getByText("nonce-exec-1")).toBeInTheDocument();
    expect(screen.getByText("policy_reject")).toBeInTheDocument();
    expect(screen.getByText("account")).toBeInTheDocument();
    expect(screen.getByText("daily_loss")).toBeInTheDocument();
    expect(screen.getAllByText("Daily loss threshold reached").length).toBeGreaterThan(0);
    expect(screen.getByText("operator@example.test")).toBeInTheDocument();
    // Even a legacy or hostile token carrying a lock field must not surface it.
    expect(screen.queryByText("opaque-lock")).not.toBeInTheDocument();
    expect(screen.getByText("-0.25 USD")).toBeInTheDocument();
    expect(screen.getByText("Recorded result")).toBeInTheDocument();
    expect(screen.getByText("Fill lock price")).toBeInTheDocument();
    expect(screen.getByText("-0.30 USD")).toBeInTheDocument();
    expect(screen.getByText("Block 1")).toBeInTheDocument();
    expect(
      screen.getByText(
        "desk-alpha · daily_loss · Daily loss threshold reached · limit=-1000",
      ),
    ).toBeInTheDocument();
  });

  it("surfaces an unknown key id (404) as an inline error", async () => {
    const fixture = await buildSignedToken("key-gone");
    fetchPublicKeyByIdMock.mockRejectedValue(
      new ApiError("not found", "not_found", 404),
    );

    const user = userEvent.setup();
    renderPanel(null, null, "verify");

    const area = await screen.findByLabelText("Token", {
      selector: "textarea",
    });
    await user.click(area);
    await user.paste(fixture.token);
    await user.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() =>
      expect(
        screen.getByText(/No public key is known for key id key-gone/),
      ).toBeInTheDocument(),
    );
  });

  it("rejects a malformed token with an inline error", async () => {
    const user = userEvent.setup();
    renderPanel(null, null, "verify");

    const area = await screen.findByLabelText("Token", {
      selector: "textarea",
    });
    await user.click(area);
    await user.paste("not-a-real-token");
    await user.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() =>
      expect(screen.getByText("ERROR")).toBeInTheDocument(),
    );
    expect(fetchPublicKeyByIdMock).not.toHaveBeenCalled();
  });

  it("treats an alg-none token as unsigned without fetching a key", async () => {
    const fixture = await buildSignedToken("", { alg: "none", keyId: "" });
    const user = userEvent.setup();
    renderPanel(null, null, "verify");

    const area = await screen.findByLabelText("Token", {
      selector: "textarea",
    });
    await user.click(area);
    await user.paste(fixture.token);
    await user.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() =>
      expect(screen.getByText("UNSIGNED")).toBeInTheDocument(),
    );
    expect(fetchPublicKeyByIdMock).not.toHaveBeenCalled();
  });

  it("opens directly in verify mode with no reproduction tab", () => {
    renderPanel(null, null, "verify");
    expect(
      screen.queryByRole("tab", { name: "Reproduction" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("tab", { name: "Verify token" }),
    ).toBeInTheDocument();
  });
});

describe("OrderVerificationPanel — mode switching", () => {
  it("switches from reproduction to verify token", async () => {
    const fixture = await buildSignedToken("key-1");
    fetchEventReproductionMock.mockResolvedValue(submitBundleFrom(fixture));

    const user = userEvent.setup();
    renderPanel("ord-repro-1", "evt-repro-1");

    await screen.findByText("Token");
    await user.click(screen.getByRole("tab", { name: "Verify token" }));

    // The paste textarea placeholder confirms the verify-token pane is active.
    expect(
      screen.getByPlaceholderText("Paste a base64url token…"),
    ).toBeInTheDocument();
  });
});
