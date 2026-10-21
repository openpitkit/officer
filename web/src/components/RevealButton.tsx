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

import { Eye, EyeOff } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";

/**
 * Show/Hide toggle for a masked secret. Controlled: the caller holds whether
 * the value is `shown` and flips it in `onToggle`.
 */
export function RevealButton({
  shown,
  onToggle,
  disabled,
  variant = "outline",
}: {
  shown: boolean;
  onToggle: () => void;
  disabled?: boolean;
  variant?: "outline" | "ghost";
}) {
  const { t } = useTranslation();

  return (
    <Button
      type="button"
      variant={variant}
      size="sm"
      disabled={disabled}
      onClick={onToggle}
    >
      {shown ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
      {shown ? t("actions.hide") : t("actions.show")}
    </Button>
  );
}
