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

import { mergeConfig, defineConfig } from "vitest/config";

import viteConfig from "./vite.config";

// The test timeout is a hang detector, not a speed budget. The gate runs this
// suite beside the Go race tests and lint, often in several gates at once, and
// there a test takes up to 20 times its idle time: a 160 ms test took 1.5 s
// and a 600 ms page test passed 5 s, the default timeout. 20 s keeps about
// four times the room over the slowest test measured under that load;
// src/test/setup.ts widens the async-utility wait on the same grounds.
const testTimeout = 20_000;

// Inherit the app's resolver (the "@" alias) from vite.config so test imports
// resolve exactly like the build, then layer the jsdom + globals test setup on
// top. The resolver is not restated here on purpose.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: "jsdom",
      globals: true,
      setupFiles: ["./src/test/setup.ts"],
      testTimeout,
      // The reporter lists every test slower than this: one that spends a
      // quarter of its timeout fails once the load grows fourfold, so the
      // list names the next test to make cheaper before it reddens a gate.
      slowTestThreshold: testTimeout / 4,
      coverage: {
        provider: "v8",
        include: ["src/**/*.{ts,tsx}"],
      },
    },
  }),
);
