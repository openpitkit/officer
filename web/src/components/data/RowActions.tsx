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

import {
  ArrowLeftRight,
  Ban,
  BriefcaseBusiness,
  Check,
  Copy,
  Eye,
  Filter,
  GitFork,
  History,
  Link,
  Pencil,
  Receipt,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import type { CSSProperties, MouseEvent, ReactNode } from "react";
import { useCallback, useEffect, useState } from "react";

import { copyText } from "@/lib/clipboard";
import { cn } from "@/lib/utils";
import { useOpenInNewTabHint } from "./openInNewTabHint";

type ActionButtonClick = (event: MouseEvent<HTMLButtonElement>) => void;

export interface ActionButtonProps {
  /** Glyph key: copy-id | share | view | filter-by | clone | edit | block | trash | history | check | positions | trading | trades | policies. */
  icon: string;
  /** Tooltip + aria-label (required for accessibility). */
  title: string;
  /** Force the active (accent) look. */
  active?: boolean;
  /** Use the danger hue on hover/active (block, delete). */
  danger?: boolean;
  href?: string;
  newTabHint?: string;
  onClick?: ActionButtonClick;
  /** Square hit size in px (default 30; pass 44 for touch builds). */
  size?: number;
  style?: CSSProperties;
}

const ICONS = {
  "copy-id": Copy,
  share: Link,
  view: Eye,
  "filter-by": Filter,
  clone: GitFork,
  edit: Pencil,
  block: Ban,
  trash: Trash2,
  history: History,
  check: Check,
  positions: BriefcaseBusiness,
  trading: ArrowLeftRight,
  trades: Receipt,
  policies: ShieldCheck,
};

function shouldOpenInNewTab(event: MouseEvent): boolean {
  return event.metaKey || event.ctrlKey;
}

function openInNewTab(href: string) {
  if (typeof window === "undefined") {
    return;
  }
  window.open(href, "_blank", "noopener,noreferrer");
}

/** Generic icon-action button - composes the named actions below. */
export function ActionButton({
  icon,
  title,
  active = false,
  danger = false,
  href,
  newTabHint,
  onClick,
  size = 30,
  style,
}: ActionButtonProps) {
  const Icon = ICONS[icon as keyof typeof ICONS] ?? Copy;
  const tooltip = href && newTabHint ? `${title}\n${newTabHint}` : title;

  return (
    <button
      type="button"
      title={tooltip}
      aria-label={title}
      className={cn(
        "inline-flex shrink-0 items-center justify-center rounded-badge bg-transparent text-muted transition-colors hover:bg-accent-dim hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        active && "bg-accent-dim text-accent",
        danger &&
          "hover:bg-[var(--danger-dim)] hover:text-[var(--danger)]",
      )}
      style={{
        width: `min(${size}px, var(--dens-row-action-size, ${size}px))`,
        height: `min(${size}px, var(--dens-row-action-size, ${size}px))`,
        ...style,
      }}
      onClick={(event) => {
        event.stopPropagation();
        if (href !== undefined && shouldOpenInNewTab(event)) {
          openInNewTab(href);
          return;
        }
        onClick?.(event);
      }}
    >
      <Icon
        aria-hidden="true"
        style={{
          height: size <= 24 ? 12 : 14,
          width: size <= 24 ? 12 : 14,
        }}
      />
    </button>
  );
}

export interface CopyIdButtonProps {
  /** The ID string written to the clipboard. */
  value: string | number;
  title?: string;
  /** Title shown during the post-copy flash. */
  copiedTitle?: string;
  onCopy?: (value: string | number) => void;
  size?: number;
}

function useCopyFeedback() {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) {
      return undefined;
    }
    const timeout = window.setTimeout(() => setCopied(false), 1400);
    return () => window.clearTimeout(timeout);
  }, [copied]);

  const markCopied = useCallback(() => setCopied(true), []);
  return [copied, markCopied] as const;
}

/** Copy the raw entity ID to the clipboard. */
export function CopyIdButton({
  value,
  title = "Copy ID",
  copiedTitle = "Copied",
  onCopy,
  size = 30,
}: CopyIdButtonProps) {
  const [copied, markCopied] = useCopyFeedback();

  return (
    <ActionButton
      icon={copied ? "check" : "copy-id"}
      active={copied}
      title={copied ? copiedTitle : title}
      size={size}
      onClick={() => {
        void copyText(String(value)).then(() => {
          onCopy?.(value);
          markCopied();
        });
      }}
    />
  );
}

export interface ShareLinkButtonProps {
  /** Deep-link URL copied to the clipboard. */
  href: string;
  title?: string;
  /** Title shown during the post-copy flash. */
  copiedTitle?: string;
  onCopy?: (href: string) => void;
  size?: number;
}

/** Copy a deep-link URL that opens this entity / list-with-current-filter. */
export function ShareLinkButton({
  href,
  title = "Copy link",
  copiedTitle = "Link copied",
  onCopy,
  size = 30,
}: ShareLinkButtonProps) {
  const newTabHint = useOpenInNewTabHint();
  const [copied, markCopied] = useCopyFeedback();

  return (
    <ActionButton
      icon={copied ? "check" : "share"}
      active={copied}
      title={copied ? copiedTitle : title}
      href={href}
      newTabHint={newTabHint}
      size={size}
      onClick={() => {
        void copyText(href).then(() => {
          onCopy?.(href);
          markCopied();
        });
      }}
    />
  );
}

/** Props for a row action that opens a link (cmd/ctrl-click new tab). */
export interface RowActionProps {
  href?: string;
  onClick?: ActionButtonClick;
  title?: string;
  size?: number;
  style?: CSSProperties;
}

/** Props for a row action that only fires an onClick and has no navigable
 *  target, so it never accepts `href`/`newTabHint`. Tightened so a caller
 *  cannot pass link props a click-only button would silently ignore. */
export type RowActionClickProps = Omit<RowActionProps, "href">;

/** Open the entity's own surface / related records. */
export function ViewEntityButton({
  href,
  onClick,
  title = "View",
  size = 30,
}: RowActionProps) {
  const newTabHint = useOpenInNewTabHint();
  return (
    <ActionButton
      icon="view"
      title={title}
      href={href}
      newTabHint={newTabHint}
      onClick={onClick}
      size={size}
    />
  );
}

/** Apply a filter for this entity in the current table. */
export function FilterByButton({
  onClick,
  href,
  title = "Filter by this",
  size = 30,
}: RowActionProps) {
  const newTabHint = useOpenInNewTabHint();
  return (
    <ActionButton
      icon="filter-by"
      title={title}
      href={href}
      newTabHint={newTabHint}
      onClick={onClick}
      size={size}
    />
  );
}

/** Create a new record derived from this one. */
export function CloneButton({
  onClick,
  title = "Clone",
  size = 30,
}: RowActionClickProps) {
  return <ActionButton icon="clone" title={title} onClick={onClick} size={size} />;
}

export function EditButton({
  onClick,
  title = "Edit",
  size = 30,
  style,
}: RowActionClickProps) {
  return (
    <ActionButton
      icon="edit"
      title={title}
      onClick={onClick}
      size={size}
      style={style}
    />
  );
}

export function BlockButton({
  onClick,
  title = "Block",
  size = 30,
}: RowActionClickProps) {
  return (
    <ActionButton
      icon="block"
      title={title}
      onClick={onClick}
      size={size}
      danger
    />
  );
}

export function DeleteButton({
  onClick,
  title = "Delete",
  size = 30,
}: RowActionClickProps) {
  return (
    <ActionButton
      icon="trash"
      title={title}
      onClick={onClick}
      size={size}
      danger
    />
  );
}

export function HistoryButton({
  href,
  onClick,
  title = "History",
  size = 30,
}: RowActionProps) {
  const newTabHint = useOpenInNewTabHint();
  return (
    <ActionButton
      icon="history"
      title={title}
      href={href}
      newTabHint={newTabHint}
      onClick={onClick}
      size={size}
    />
  );
}

/** Open the entity's positions surface. */
export function PositionsButton({
  href,
  onClick,
  title = "Positions",
  size = 30,
}: RowActionProps) {
  const newTabHint = useOpenInNewTabHint();
  return (
    <ActionButton
      icon="positions"
      title={title}
      href={href}
      newTabHint={newTabHint}
      onClick={onClick}
      size={size}
    />
  );
}

/** Open the entity's trading surface. */
export function TradingButton({
  href,
  onClick,
  title = "Trading",
  size = 30,
}: RowActionProps) {
  const newTabHint = useOpenInNewTabHint();
  return (
    <ActionButton
      icon="trading"
      title={title}
      href={href}
      newTabHint={newTabHint}
      onClick={onClick}
      size={size}
    />
  );
}

/** Open the entity's trades surface. */
export function TradesButton({
  href,
  onClick,
  title = "Trades",
  size = 30,
}: RowActionProps) {
  const newTabHint = useOpenInNewTabHint();
  return (
    <ActionButton
      icon="trades"
      title={title}
      href={href}
      newTabHint={newTabHint}
      onClick={onClick}
      size={size}
    />
  );
}

/** Open the entity's policies surface. */
export function PoliciesButton({
  href,
  onClick,
  title = "Policies",
  size = 30,
}: RowActionProps) {
  const newTabHint = useOpenInNewTabHint();
  return (
    <ActionButton
      icon="policies"
      title={title}
      href={href}
      newTabHint={newTabHint}
      onClick={onClick}
      size={size}
    />
  );
}

export interface RowActionsProps {
  children?: ReactNode;
  gap?: number;
  align?: "flex-start" | "center" | "flex-end";
  style?: CSSProperties;
}

/** Fixed-order container for a row's action buttons. */
export function RowActions({
  children,
  gap = 4,
  align = "flex-end",
  style,
}: RowActionsProps) {
  return (
    <span
      className="inline-flex items-center"
      style={{ gap, justifyContent: align, ...style }}
    >
      {children}
    </span>
  );
}

export interface IdCellProps {
  /** ID string (also the copied value unless children override the display). */
  value: string | number;
  children?: ReactNode;
  mono?: boolean;
  /** Title for the inline copy button. */
  copyTitle?: string;
  /** Title shown during the inline copy button's post-copy flash. */
  copiedTitle?: string;
  onCopy?: (value: string | number) => void;
  gap?: number;
  style?: CSSProperties;
}

/** An ID value with an inline 22px copy button. */
export function IdCell({
  value,
  children,
  mono = true,
  copyTitle,
  copiedTitle,
  onCopy,
  gap = 7,
  style,
}: IdCellProps) {
  return (
    <span className="inline-flex min-w-0 items-center" style={{ gap, ...style }}>
      <span className={cn("min-w-0 truncate", mono && "nums")}>
        {children ?? value}
      </span>
      <span className="inline-flex shrink-0 items-center">
        <CopyIdButton
          value={value}
          title={copyTitle}
          copiedTitle={copiedTitle}
          onCopy={onCopy}
          size={22}
        />
      </span>
    </span>
  );
}
