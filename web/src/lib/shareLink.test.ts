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

import { describe, expect, it } from "vitest";

import { absoluteAppUrl, shareUrl } from "@/lib/shareLink";

describe("shareLink", () => {
  it("resolves in-app paths against the current origin", () => {
    expect(absoluteAppUrl("/accounts?code=desk")).toBe(
      `${window.location.origin}/accounts?code=desk`,
    );
  });

  it("keeps clean share URLs query-free until filters are present", () => {
    expect(shareUrl("/assets", new URLSearchParams())).toBe(
      `${window.location.origin}/assets`,
    );

    const params = new URLSearchParams();
    params.set("sort", "code");
    params.set("order", "asc");
    expect(shareUrl("/assets", params)).toBe(
      `${window.location.origin}/assets?sort=code&order=asc`,
    );
  });
});
