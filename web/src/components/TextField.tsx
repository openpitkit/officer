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

import type { InputHTMLAttributes, ReactNode, Ref } from "react";

import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

/** A labelled text or password field with an optional error under it. */
export function TextField({
  ref,
  id,
  label,
  type,
  value,
  placeholder,
  disabled,
  required,
  onChange,
  error,
  autoComplete,
  verbatim = false,
  action,
}: {
  /** The input element, for a caller that moves the focus to it. */
  ref?: Ref<HTMLInputElement>;
  id: string;
  label: string;
  type: "text" | "password";
  value: string;
  placeholder?: string;
  disabled: boolean;
  required?: boolean;
  onChange: (value: string) => void;
  /** A message under the input that marks the value invalid. */
  error?: string;
  /** The autofill hint, such as `current-password` or `one-time-code`. */
  autoComplete?: InputHTMLAttributes<HTMLInputElement>["autoComplete"];
  /**
   * Takes the value as typed: no spell check, which can send the text of a
   * field to an online spell checker, no autocorrection and no
   * autocapitalisation.
   */
  verbatim?: boolean;
  /** A control beside the input, such as a Show/Hide toggle. */
  action?: ReactNode;
}) {
  const errorId = `${id}-error`;
  const input = (
    <Input
      ref={ref}
      id={id}
      type={type}
      value={value}
      placeholder={placeholder}
      autoComplete={autoComplete}
      spellCheck={verbatim ? false : undefined}
      autoCorrect={verbatim ? "off" : undefined}
      autoCapitalize={verbatim ? "off" : undefined}
      aria-invalid={error === undefined ? undefined : true}
      aria-describedby={error === undefined ? undefined : errorId}
      className={cn(
        action !== undefined && "min-w-0 flex-1",
        error !== undefined && "border-[var(--danger)]",
      )}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      required={required}
    />
  );
  return (
    <div className="flex flex-col gap-y-2">
      <Label htmlFor={id}>{label}</Label>
      {action === undefined ? (
        input
      ) : (
        <div className="flex items-center gap-2">
          {input}
          {action}
        </div>
      )}
      {error !== undefined && (
        <p id={errorId} className="text-xs text-[var(--danger)]">
          {error}
        </p>
      )}
    </div>
  );
}
