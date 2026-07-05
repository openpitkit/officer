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

import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { Button, type ButtonProps } from "@/components/ui/button";
import { cn } from "@/lib/utils";

/**
 * Action button for use inside a table row. Renders the icon plus a label that
 * is hidden at terminal (Dense) density, where the button contracts to an
 * icon-only square. The label always remains the accessible name (`title` +
 * `aria-label`), so the control stays usable and screen-reader friendly when
 * the visible text is collapsed.
 *
 * Use this for any label-bearing row action; the `.row-action` /
 * `.row-action-label` convention it relies on lives in `index.css`. Icon-only
 * status controls do not need it.
 */
export function RowActionButton({
  icon: Icon,
  label,
  className,
  variant = "outline",
  size = "sm",
  children,
  ...props
}: {
  icon: LucideIcon;
  label: string;
  children?: ReactNode;
} & Omit<ButtonProps, "children">) {
  return (
    <Button
      variant={variant}
      size={size}
      className={cn("row-action", className)}
      title={label}
      aria-label={label}
      {...props}
    >
      <Icon className="h-3.5 w-3.5" aria-hidden="true" />
      <span className="row-action-label">{children ?? label}</span>
    </Button>
  );
}
