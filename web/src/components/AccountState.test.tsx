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

import { render, screen } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";

import { AccountStatusBadge } from "@/components/AccountState";
import i18n from "@/i18n";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("AccountStatusBadge", () => {
  it.each([
    { blocked: true, label: "blocked", tone: "danger" },
    { blocked: false, label: "active", tone: "ok" },
  ])(
    "shows $label in the $tone tone when blocked is $blocked",
    ({ blocked, label, tone }) => {
      render(<AccountStatusBadge blocked={blocked} />);

      const badge = screen.getByText(label);
      expect(badge).toHaveClass(
        `border-[var(--${tone})]`,
        `text-[var(--${tone})]`,
      );
      expect(badge.querySelector(".rounded-full")).toHaveClass(
        `bg-[var(--${tone})]`,
      );
    },
  );
});
