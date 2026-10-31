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

import type { TradingAccessSubject } from "@/api/types";
import { accessGrantBlock } from "./accessGrantBlock";

const ib = { venueAccounts: true, unlinkedFees: false };
const alpaca = { venueAccounts: false, unlinkedFees: true };
const empty = { feeAccount: "", access: [] };
const bound = {
  account: "alpha",
  accountGroup: "",
  venueAccount: "DU123",
  group: "",
};

describe("accessGrantBlock", () => {
  it.each<TradingAccessSubject>([
    { account: "alpha" },
    { accountGroup: "desk" },
  ])(
    "requires a venue account for %j before any fee-account rule",
    (subject) => {
      for (const venueAccount of [undefined, "", "   "]) {
        for (const feeAccount of ["", "fees"]) {
          expect(
            accessGrantBlock(
              ib,
              { ...empty, feeAccount },
              { ...subject, venueAccount },
            ),
          ).toBe("needsVenueAccount");
          expect(
            accessGrantBlock(
              { ...ib, unlinkedFees: true },
              { ...empty, feeAccount },
              { ...subject, venueAccount },
            ),
          ).toBe("needsVenueAccount");
        }
      }
      expect(
        accessGrantBlock(ib, empty, { ...subject, venueAccount: " DU123 " }),
      ).toBeNull();
    },
  );

  it("does not use another grant's venue account", () => {
    expect(
      accessGrantBlock(ib, { ...empty, access: [bound] }, { account: "alpha" }),
    ).toBe("needsVenueAccount");
  });

  it("blocks groups and second accounts for unlinked fees", () => {
    expect(accessGrantBlock(alpaca, empty, { accountGroup: "desk" })).toBe(
      "groupNeedsFeeAccount",
    );
    expect(
      accessGrantBlock(
        alpaca,
        { ...empty, access: [bound] },
        { account: "beta" },
      ),
    ).toBe("secondAccountNeedsFeeAccount");
  });

  it("allows a first account, regrant, a configured fee account and a provider needing neither field", () => {
    expect(accessGrantBlock(alpaca, empty, { account: "alpha" })).toBeNull();
    expect(
      accessGrantBlock(
        alpaca,
        { ...empty, access: [bound] },
        { account: "alpha" },
      ),
    ).toBeNull();
    expect(
      accessGrantBlock(
        alpaca,
        { ...empty, feeAccount: "fees" },
        { accountGroup: "desk" },
      ),
    ).toBeNull();
    expect(
      accessGrantBlock({ venueAccounts: false, unlinkedFees: false }, empty, {
        accountGroup: "desk",
      }),
    ).toBeNull();
  });
});
