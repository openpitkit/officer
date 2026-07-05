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

import { X } from "lucide-react";

import { Button } from "@/components/ui/button";

/**
 * Ghost icon button that clears a single filter field. Render it only when the
 * field holds a non-default value; clicking should clear that field and apply.
 *
 * The caller owns the label (typically `t("common:filters.clearField")`) so it
 * can stay namespace-agnostic across pages; it is used as both the tooltip and
 * the accessible name.
 */
export function ClearFieldButton({
  label,
  onClick,
  disabled,
}: {
  label: string;
  onClick: () => void;
  disabled?: boolean;
}) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon"
      className="h-8 w-8 shrink-0"
      onClick={onClick}
      disabled={disabled}
      title={label}
      aria-label={label}
    >
      <X className="h-3.5 w-3.5" />
    </Button>
  );
}
