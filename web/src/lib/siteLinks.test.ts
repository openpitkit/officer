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

import { siteLink } from "@/lib/siteLinks";

describe("siteLink", () => {
  it("marks site roots without adding a slash", () => {
    expect(siteLink("officer")).toBe("https://officer.openpit.dev?officer");
    expect(siteLink("openpit")).toBe("https://openpit.dev?officer");
    expect(siteLink("wiki")).toBe("https://wiki.openpit.dev?officer");
  });

  it("preserves the path", () => {
    expect(siteLink("officer", "/questions/")).toBe(
      "https://officer.openpit.dev/questions/?officer",
    );
  });

  it("appends the marker to an existing query", () => {
    expect(siteLink("officer", "/questions/?lang=en")).toBe(
      "https://officer.openpit.dev/questions/?lang=en&officer",
    );
  });

  it("puts the marker before a fragment", () => {
    expect(siteLink("officer", "/video-guides/", "guide-quickstart")).toBe(
      "https://officer.openpit.dev/video-guides/?officer#guide-quickstart",
    );
    expect(siteLink("wiki", "/Policies/?lang=en", "ratelimitpolicy")).toBe(
      "https://wiki.openpit.dev/Policies/?lang=en&officer#ratelimitpolicy",
    );
    expect(siteLink("openpit", undefined, "sdk")).toBe(
      "https://openpit.dev?officer#sdk",
    );
  });

  it("rejects an empty fragment", () => {
    expect(() => siteLink("officer", "/video-guides/", "")).toThrow(
      new TypeError('Site link fragment must be nonempty and contain no "#"'),
    );
  });

  it("rejects a fragment containing a hash", () => {
    expect(() => siteLink("wiki", "/Policies/", "#ratelimitpolicy")).toThrow(
      new TypeError('Site link fragment must be nonempty and contain no "#"'),
    );
  });

  it.each(["", "questions/", "/questions/#title"])(
    "rejects malformed path %j",
    (path) => {
      expect(() => siteLink("officer", path)).toThrow(
        new TypeError('Site link path must start with "/" and contain no "#"'),
      );
    },
  );
});
