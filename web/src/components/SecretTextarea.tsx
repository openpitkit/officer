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

import { useState, type ComponentProps } from "react";

import { RevealButton } from "@/components/RevealButton";
import { Textarea } from "@/components/ui/textarea";
import { cn } from "@/lib/utils";

/**
 * A masked multiline secret field with a Show/Hide toggle, without autofill or
 * spell checking. It stays a textarea, which keeps the line breaks of a pasted
 * PEM key, and masks the characters with `-webkit-text-security` instead.
 */
export function SecretTextarea(
  props: Omit<
    ComponentProps<typeof Textarea>,
    "rows" | "className" | "autoComplete" | "autoCorrect" | "spellCheck"
  >,
) {
  const [shown, setShown] = useState(false);
  return (
    <div className="flex items-start gap-2">
      <Textarea
        {...props}
        rows={5}
        className={cn(
          "min-w-0 flex-1 resize-none font-mono text-xs",
          !shown && "[-webkit-text-security:disc]",
        )}
        autoComplete="off"
        autoCorrect="off"
        spellCheck={false}
      />
      <RevealButton
        shown={shown}
        onToggle={() => setShown(!shown)}
        disabled={props.disabled}
        variant="ghost"
      />
    </div>
  );
}
