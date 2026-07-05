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

import { mkdir, readdir, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const distURL = new URL("../dist/", import.meta.url);
const distDir = fileURLToPath(distURL);
const placeholderName = "embed-placeholder.txt";
const placeholderText =
  "This placeholder keeps web/dist non-empty so Go //go:embed web/dist compiles\n" +
  "before the SPA bundle is built. Production builds replace this directory with\n" +
  "the generated Vite assets.\n";

await mkdir(distDir, { recursive: true });

for (const entry of await readdir(distDir)) {
  if (entry === placeholderName) {
    continue;
  }
  await rm(join(distDir, entry), { force: true, recursive: true });
}

await writeFile(join(distDir, placeholderName), placeholderText);
