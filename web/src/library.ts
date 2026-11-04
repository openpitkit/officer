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

// Entry of the library build only. The bundle is the framework barrel; the
// stylesheet imported here is extracted into lib/styles.css, which a host
// imports once as "@openpit/officer-web/styles.css". Declarations come from
// src/index.ts, so this side-effect import never reaches lib/*.d.ts.
import "./index.css";

export * from "./framework";
