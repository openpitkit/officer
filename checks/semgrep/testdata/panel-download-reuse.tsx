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

import { downloadBlob } from "@/lib/download";

function copiedDownload(blob, filename) {
  const href = URL.createObjectURL(blob);
  // ruleid: shared-panel-download
  const anchor = document.createElement("a");
  anchor.href = href;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(href);
}

function copiedShortDownload(url, filename) {
  // ruleid: shared-panel-download
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  a.click();
}

function copiedLetDownload(url, filename) {
  // ruleid: shared-panel-download
  let anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
}

function copiedVarDownload(url, filename) {
  // ruleid: shared-panel-download
  var anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
}

function sharedDownload(blob, filename) {
  // ok: shared-panel-download
  downloadBlob(blob, filename);
}

function plainLink(url) {
  // ok: shared-panel-download
  const link = document.createElement("a");
  link.href = url;
  link.click();
}

function otherElement(filename) {
  // ok: shared-panel-download
  const input = document.createElement("input");
  input.download = filename;
}
