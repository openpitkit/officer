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

import { afterEach, describe, expect, it, vi } from "vitest";

import type { AuditDetail } from "@/api/types";
import i18n from "@/i18n";
import { auditDetailText } from "@/i18n/auditDetail";
import { LOCALE_CODES } from "@/i18n/locales";

function detail(
  code: string,
  params: Record<string, string>,
  text = "",
  fields: AuditDetail["fields"] = [],
): AuditDetail {
  return { text, code, params, fields };
}

const regroup = detail(
  "set_account_group",
  { account: "desk-alpha", from: "<none>", to: "risk" },
  "Account desk-alpha group changed: <none> → risk",
);

describe("auditDetailText", () => {
  afterEach(async () => {
    vi.restoreAllMocks();
    await i18n.changeLanguage("en");
  });

  it("reads as the stored text in en when no value is a code", () => {
    expect(
      auditDetailText(
        detail(
          "create_account",
          { account: "desk-alpha" },
          "Account desk-alpha created",
        ),
      ),
    ).toBe("Account desk-alpha created");
  });

  it("fills the template of the current language and labels a sentinel", async () => {
    expect(auditDetailText(regroup)).toBe(
      "Account desk-alpha group changed: default group → risk",
    );

    await i18n.changeLanguage("ru");

    expect(auditDetailText(regroup)).toBe(
      "Изменение группы счёта desk-alpha: группа по умолчанию → risk",
    );
  });

  it("reads a limit change as a sentence in en", () => {
    const limit = detail(
      "set_limit",
      { policy: "rate_limit", scope: "account" },
      "Limit set: rate_limit (account) account=desk-alpha max_orders=5",
      [
        { name: "account", value: "desk-alpha" },
        { name: "max_orders", value: "5" },
      ],
    );

    expect(auditDetailText(limit)).toBe(
      "Limit set: Rate limit (Account) account=desk-alpha max orders=5",
    );
  });

  it("labels code values, field names and boolean values", async () => {
    await i18n.changeLanguage("es");

    const report = detail(
      "execution_report",
      {
        order: "ord-1",
        account: "desk-alpha",
        base: "BTC",
        quote: "USD",
        qty: "2",
        leavesQty: "0",
        status: "filled",
        blocks: "0",
      },
      "",
      [
        { name: "overfill", value: "true" },
        { name: "preReportReserveQty", value: "1" },
      ],
    );

    expect(auditDetailText(report)).toBe(
      "Informe de ejecución de la orden ord-1, cuenta desk-alpha, BTC/USD: " +
        "cant. 2, pendiente 0, Ejecutada, bloqueos: 0 " +
        "sobreejecución=sí reserva previa al informe=1",
    );
  });

  it("shows a detail without a code as its stored text", async () => {
    await i18n.changeLanguage("ru");

    expect(auditDetailText(detail("", {}, "manual note"))).toBe("manual note");
  });

  it("shows the stored text of a code with no template and warns once", async () => {
    await i18n.changeLanguage("es");
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const future = detail("future_detail", { account: "a" }, "future a");

    expect(auditDetailText(future)).toBe("future a");
    expect(auditDetailText(future)).toBe("future a");
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith(
      'No catalog template for audit detail "future_detail" (audit:detail.future_detail); showing the stored text.',
    );
  });

  it("shows interpolation syntax inside a value as written", () => {
    const reason = "{{account}} $t(audit:title)";

    expect(
      auditDetailText(
        detail("block_account_reason", { account: "desk-alpha", reason }),
      ),
    ).toBe(`Account desk-alpha blocked: ${reason}`);
  });
});

const EN_TEMPLATES = i18n.getResource("en", "audit", "detail") as Record<
  string,
  string
>;
const EN_FIELDS = Object.keys(
  i18n.getResource("en", "audit", "detailField") as Record<string, string>,
);
const EN_VALUES = Object.keys(
  i18n.getResource("en", "audit", "detailValue") as Record<string, string>,
);

function placeholders(template: string): string[] {
  return [...template.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort();
}

describe.each(LOCALE_CODES)("the %s audit detail catalog", (locale) => {
  it("fills every detail code with the en placeholders", () => {
    const mismatched = Object.entries(EN_TEMPLATES).filter(([code, en]) => {
      const template: unknown = i18n.getResource(
        locale,
        "audit",
        `detail.${code}`,
      );
      return (
        typeof template !== "string" ||
        template === "" ||
        placeholders(template).join() !== placeholders(en).join()
      );
    });

    expect(mismatched).toEqual([]);
  });

  it("labels every detail field and value", () => {
    const unlabelled = [
      ...EN_FIELDS.map((name) => `detailField.${name}`),
      ...EN_VALUES.map((name) => `detailValue.${name}`),
    ].filter((path) => {
      const label: unknown = i18n.getResource(locale, "audit", path);
      return typeof label !== "string" || label === "";
    });

    expect(unlabelled).toEqual([]);
  });
});
