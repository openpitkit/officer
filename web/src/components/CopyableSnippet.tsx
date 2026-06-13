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

import { Check, Copy } from "lucide-react";
import { useRef, useState } from "react";
import { useTranslation } from "react-i18next";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";

// ---------------------------------------------------------------------------

/** Read-only copyable text block with a transient "Copied" confirmation. */
export function CopyableSnippet({
  text,
  label,
  rows = 6,
  className,
}: {
  text: string;
  label?: string;
  rows?: number;
  className?: string;
}) {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);
  const taRef = useRef<HTMLTextAreaElement>(null);

  function handleCopy() {
    if (navigator.clipboard) {
      void navigator.clipboard.writeText(text).then(() => {
        showCopied();
      });
    } else {
      // Fallback for environments without Clipboard API.
      const ta = taRef.current;
      if (ta) {
        ta.select();
        // execCommand is deprecated but universally supported as fallback.
        document.execCommand("copy");
      }
      showCopied();
    }
  }

  function showCopied() {
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  return (
    <div className={cn("space-y-1.5", className)}>
      {label && (
        <div className="flex items-center justify-between gap-3">
          <p className="text-xs text-muted">{label}</p>
          <Button
            variant="outline"
            size="sm"
            onClick={handleCopy}
            aria-label={t("actions.copy")}
          >
            {copied ? (
              <Check className="h-3.5 w-3.5 text-accent" />
            ) : (
              <Copy className="h-3.5 w-3.5" />
            )}
            {copied ? t("actions.copied") : t("actions.copy")}
          </Button>
        </div>
      )}
      {!label && (
        <div className="flex justify-end">
          <Button
            variant="outline"
            size="sm"
            onClick={handleCopy}
            aria-label={t("actions.copy")}
          >
            {copied ? (
              <Check className="h-3.5 w-3.5 text-accent" />
            ) : (
              <Copy className="h-3.5 w-3.5" />
            )}
            {copied ? t("actions.copied") : t("actions.copy")}
          </Button>
        </div>
      )}
      <textarea
        ref={taRef}
        readOnly
        rows={rows}
        value={text}
        spellCheck={false}
        className="w-full rounded-card border border-border bg-muted/30 p-2 font-mono text-[0.6875rem] text-muted-lt focus:outline-none focus:ring-1 focus:ring-accent/40 resize-none select-all"
        aria-label={label ?? t("states.copyableSnippet")}
      />
    </div>
  );
}
