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

/**
 * Copy text to the clipboard, preferring the async Clipboard API and falling
 * back to a hidden `<textarea>` + `execCommand` for environments without it
 * (older browsers, non-secure contexts). Resolves once the copy completes.
 */
export async function copyText(text: string): Promise<void> {
  if (navigator.clipboard) {
    await navigator.clipboard.writeText(text);
    return;
  }
  // Fallback for environments without the Clipboard API.
  const ta = document.createElement("textarea");
  ta.value = text;
  // Keep the element out of view and out of the layout/scroll flow.
  ta.style.position = "fixed";
  ta.style.top = "-9999px";
  ta.setAttribute("readonly", "");
  document.body.appendChild(ta);
  ta.select();
  try {
    // execCommand is deprecated but universally supported as a fallback.
    document.execCommand("copy");
  } finally {
    document.body.removeChild(ta);
  }
}
