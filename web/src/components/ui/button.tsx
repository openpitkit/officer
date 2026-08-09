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

import { Slot } from "@radix-ui/react-slot";
import { cva, type VariantProps } from "class-variance-authority";
import { forwardRef, type ButtonHTMLAttributes } from "react";

import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-card text-xs font-medium transition-[background-color,border-color,color] duration-[180ms] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-bg disabled:pointer-events-none disabled:opacity-50 [&_svg]:pointer-events-none [&_svg]:size-[var(--dens-button-glyph)] [&_svg]:shrink-0",
  {
    variants: {
      variant: {
        default:
          "border border-accent bg-accent text-bg hover:border-accent-2 hover:bg-accent-2",
        outline:
          "border border-border bg-transparent text-text hover:border-border-hover hover:bg-card-hover-bg hover:text-accent",
        ghost:
          "border border-transparent bg-transparent text-muted-lt hover:bg-accent-dim hover:text-accent",
        danger:
          "border border-[var(--danger)] bg-[var(--danger)] text-bg hover:border-[var(--danger)] hover:bg-[var(--danger)]",
      },
      size: {
        default: "h-[var(--dens-button-h)] px-[var(--dens-button-px)] py-1",
        sm: "h-[var(--dens-button-sm-h)] px-[var(--dens-button-sm-px)] text-xs",
        icon: "h-[var(--dens-button-icon)] w-[var(--dens-button-icon)]",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "default",
    },
  },
);

export interface ButtonProps
  extends
    ButtonHTMLAttributes<HTMLButtonElement>,
    VariantProps<typeof buttonVariants> {
  /** Render the child element as the button (Radix Slot composition). */
  asChild?: boolean;
}

const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, asChild = false, ...props }, ref) => {
    const Comp = asChild ? Slot : "button";
    return (
      <Comp
        ref={ref}
        className={cn(buttonVariants({ variant, size }), className)}
        {...props}
      />
    );
  },
);
Button.displayName = "Button";

// `buttonVariants` is a cva() result, not a literal constant, so it is not
// covered by `allowConstantExport`; shadcn/ui co-locates it with the component.
// eslint-disable-next-line react-refresh/only-export-components
export { Button, buttonVariants };
