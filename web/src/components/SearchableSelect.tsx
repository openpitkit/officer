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

import { useEffect, useRef, useState } from "react";
import { ChevronDown } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuTrigger,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
} from "@/components/ui/dropdown-menu";

/** Select a listed value; text entered in the popup only filters the choices. */
export function SearchableSelect({
  id,
  value,
  onChange,
  options,
  disabled,
  placeholder,
  searchLabel,
  emptyLabel,
}: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  options: readonly string[];
  disabled: boolean;
  placeholder: string;
  searchLabel: string;
  emptyLabel: string;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const search = useRef<HTMLInputElement>(null);
  const firstOption = useRef<HTMLDivElement>(null);
  const matches = options.filter((option) =>
    option.toLowerCase().includes(query.trim().toLowerCase()),
  );
  useEffect(() => {
    if (open) search.current?.focus();
  }, [open]);
  return (
    <DropdownMenu
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        setQuery("");
      }}
    >
      <DropdownMenuTrigger asChild disabled={disabled}>
        <Button
          id={id}
          variant="outline"
          disabled={disabled}
          className="w-full justify-between bg-surface-2 font-normal"
        >
          <span className={value ? undefined : "text-muted"}>
            {value || placeholder}
          </span>
          <ChevronDown className="h-4 w-4 shrink-0 text-muted" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        className="w-[var(--radix-dropdown-menu-trigger-width)]"
      >
        <div className="p-1">
          <Input
            ref={search}
            type="search"
            aria-label={searchLabel}
            placeholder={searchLabel}
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Escape") return;
              event.stopPropagation();
              if (event.key === "ArrowDown" && matches.length > 0) {
                event.preventDefault();
                firstOption.current?.focus();
              }
            }}
          />
        </div>
        <DropdownMenuRadioGroup
          value={value}
          onValueChange={onChange}
          className="max-h-56 overflow-y-auto"
        >
          {matches.map((option, index) => (
            <DropdownMenuRadioItem
              key={option}
              value={option}
              ref={index === 0 ? firstOption : undefined}
            >
              {option}
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
        {matches.length === 0 && (
          <p role="status" className="px-2 py-3 text-xs text-muted-lt">
            {emptyLabel}
          </p>
        )}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
