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

import { Check, Copy } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { copyText } from "@/lib/clipboard";
import { useCopyFeedback } from "@/lib/useCopyFeedback";

/**
 * A labelled button that copies `text` to the clipboard and reads "Copied"
 * for 1500 ms after it; its accessible name is the label it shows.
 */
export function CopyButton({ text }: { text: string }) {
  const { t } = useTranslation();
  const [copied, markCopied] = useCopyFeedback(1500);
  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      onClick={() => {
        void copyText(text).then(markCopied);
      }}
    >
      {copied ? (
        <Check aria-hidden="true" className="h-3.5 w-3.5 text-accent" />
      ) : (
        <Copy aria-hidden="true" className="h-3.5 w-3.5" />
      )}
      {copied ? t("actions.copied") : t("actions.copy")}
    </Button>
  );
}
