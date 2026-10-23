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

import { describe, expect, it } from "vitest";

import type { CredentialStorage, SigningKey } from "@/api/types";
import {
  accountModeValue,
  encryptionValue,
  signingValue,
} from "@/components/security/storageValues";
import i18n from "@/i18n";
import { formatDateTime } from "@/i18n/format";

const t = i18n.getFixedT("en", "service");

const SEALED_AT = "2026-10-15T09:00:00Z";

function storage(
  credentialsSealed: boolean,
  sealedAt: string | null,
): CredentialStorage {
  return {
    credentialsSealed,
    plaintextTradingKeys: false,
    plaintextMarketDataKeys: false,
    sealedAt,
    resealedSecrets: 0,
  };
}

const ACTIVE_KEY: SigningKey = {
  keyId: "test-key-id-1234",
  fingerprint: "ab:cd:ef:01",
  createdAt: "2026-01-01T00:00:00Z",
  active: true,
};

describe("encryptionValue", () => {
  it("names stored keys plain text while the database is not sealed", () => {
    expect(encryptionValue(t, storage(false, null))).toBe(
      "Off · stored keys are plain text",
    );
  });

  it("names encryption on when sealing re-encrypted nothing", () => {
    expect(encryptionValue(t, storage(true, null))).toBe("On");
  });

  it("names when sealing re-encrypted the stored values", () => {
    expect(encryptionValue(t, storage(true, SEALED_AT))).toBe(
      `On · sealed ${formatDateTime(SEALED_AT)}`,
    );
  });
});

describe("signingValue", () => {
  it("names signing off by the setting, with or without a key", () => {
    expect(signingValue(t, false, ACTIVE_KEY)).toBe("Off");
    expect(signingValue(t, false, null)).toBe("Off");
  });

  it("names signing on without a key to sign with", () => {
    expect(signingValue(t, true, null)).toBe("On · no signing key");
  });

  it("names the fingerprint of the active key", () => {
    expect(signingValue(t, true, ACTIVE_KEY)).toBe("On · key ab:cd:ef:01");
  });
});

describe("accountModeValue", () => {
  it("names several accounts without the default account", () => {
    expect(accountModeValue(t, true, "")).toBe("Several accounts");
  });

  it("names the one account", () => {
    expect(accountModeValue(t, false, "ACC-1")).toBe("One account · ACC-1");
  });
});
