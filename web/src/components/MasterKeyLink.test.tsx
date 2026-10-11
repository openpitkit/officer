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
import { I18nextProvider } from "react-i18next";
import { beforeEach, describe, expect, it } from "vitest";

import { MasterKeyLink } from "@/components/MasterKeyLink";
import i18n from "@/i18n";

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("MasterKeyLink", () => {
  it("opens the master-key guide in a new tab", () => {
    render(
      <I18nextProvider i18n={i18n}>
        <MasterKeyLink />
      </I18nextProvider>,
    );
    const link = screen.getByRole("link", { name: "How to set a master key" });
    expect(link).toHaveAttribute(
      "href",
      "https://github.com/openpitkit/officer/blob/main/docs/configuration.md#master-key",
    );
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", "noopener noreferrer");
  });
});
