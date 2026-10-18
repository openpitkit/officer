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

// Global test setup: register jest-dom matchers, initialize the app i18n so
// react-i18next has resources during render, show accounts by default, and
// unmount the React tree after each test to keep cases isolated.
import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach, beforeEach } from "vitest";

// Side-effect import: runs the app's i18next .init() and loads every catalog.
import "@/i18n";
import { setAccountScope } from "@/lib/accountScope";
import "@/test/registerDefaults";

// A findBy or waitFor gives up after this long. The 1 s default is a hang
// detector sized for an idle machine; under gate load a suggestion lookup
// that settles in 0.4 s idle took 1.5 s. See testTimeout in vitest.config.ts.
configure({ asyncUtilTimeout: 5_000 });

// jsdom lacks these APIs, and Radix Select calls them.
const proto = window.HTMLElement.prototype;
proto.hasPointerCapture = () => false;
proto.setPointerCapture = () => {};
proto.releasePointerCapture = () => {};
proto.scrollIntoView = () => {};

// The app sets the scope from GET /account-mode before its shell renders;
// tests that need hidden accounts set their own scope before rendering.
beforeEach(() => {
  setAccountScope({ accounts: true, account: "" });
});

afterEach(() => {
  cleanup();
});
