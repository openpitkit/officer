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
import type { ComponentPropsWithoutRef } from "react";

import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";

/**
 * Reset glyph rendered inside an input's right edge. Shared across filter
 * fields and form inputs so every clearable field looks and behaves the same.
 * `tabIndex={-1}` and the `mousedown` guard keep focus on the input.
 */
export function ClearInlineButton({
  label,
  onClick,
}: {
  label: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      tabIndex={-1}
      className="absolute right-1.5 top-1/2 inline-flex h-5 w-5 -translate-y-1/2 items-center justify-center rounded-badge text-muted transition-colors hover:bg-accent-dim hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
    >
      <X className="h-3 w-3" aria-hidden="true" />
    </button>
  );
}

export interface ClearableInputProps extends ComponentPropsWithoutRef<
  typeof Input
> {
  /** Clears the field; the inline reset glyph shows only while non-empty. */
  onClear?: () => void;
  /** Accessible name + tooltip for the inline reset glyph. */
  clearLabel?: string;
}

/** A text input with a shared inline reset glyph at its right edge. */
export function ClearableInput({
  onClear,
  clearLabel = "Clear",
  className,
  value,
  disabled,
  ...props
}: ClearableInputProps) {
  const clearable =
    onClear !== undefined && value !== undefined && value !== "" && !disabled;
  return (
    <span className="relative block">
      <Input
        value={value}
        disabled={disabled}
        className={cn(className, clearable && "pr-7")}
        {...props}
      />
      {clearable && <ClearInlineButton label={clearLabel} onClick={onClear} />}
    </span>
  );
}
