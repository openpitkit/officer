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

import { useState, type ReactNode } from "react";
import { Trans, useTranslation } from "react-i18next";

import { CopyableSnippet } from "@/components/CopyableSnippet";
import { Segmented } from "@/components/data/FilterOperators";
import { cn } from "@/lib/utils";

/** How Officer runs on this computer. */
type Deployment = "docker" | "binary";

/** The system whose terminal runs the commands. */
type System = "windows" | "mac" | "linux";

/** The shell of a system: macOS and Linux share one. */
type Shell = "posix" | "powershell";

/** The commands of the three steps, one command per line. */
interface StepCommands {
  create: string;
  keep: string;
  start: string;
}

// The commands are the access design's exact instructions, character for
// character. The key travels only as a file that PIT_OFFICER_MASTER_KEY_FILE
// names.
const POWERSHELL_CREATE = [
  'New-Item -ItemType Directory -Force "$HOME\\.pit-officer" | Out-Null',
  "$key = New-Object byte[] 32",
  "[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($key)",
  '[IO.File]::WriteAllText("$HOME\\.pit-officer\\master.key", [Convert]::ToBase64String($key))',
].join("\n");
const POWERSHELL_KEEP = 'Get-Content "$HOME\\.pit-officer\\master.key"';
const POSIX_KEEP = "cat ~/.pit-officer/master.key";

const DOCKER_KEY_OPTIONS: Record<Shell, string> = {
  posix:
    '-v "$HOME/.pit-officer/master.key:/run/secrets/pit-officer-master-key:ro" -e PIT_OFFICER_MASTER_KEY_FILE=/run/secrets/pit-officer-master-key',
  powershell:
    '-v "$HOME\\.pit-officer\\master.key:/run/secrets/pit-officer-master-key:ro" -e PIT_OFFICER_MASTER_KEY_FILE=/run/secrets/pit-officer-master-key',
};

const COMMANDS: Record<Deployment, Record<Shell, StepCommands>> = {
  docker: {
    posix: {
      create: [
        "mkdir -p -m 700 ~/.pit-officer",
        "openssl rand -base64 32 > ~/.pit-officer/master.key",
        "chmod 644 ~/.pit-officer/master.key",
      ].join("\n"),
      keep: POSIX_KEEP,
      start: [
        "docker stop pit-officer",
        "docker rm pit-officer",
        `docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data ${DOCKER_KEY_OPTIONS.posix} ghcr.io/openpitkit/officer`,
      ].join("\n"),
    },
    powershell: {
      create: POWERSHELL_CREATE,
      keep: POWERSHELL_KEEP,
      start: [
        "docker stop pit-officer",
        "docker rm pit-officer",
        `docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data ${DOCKER_KEY_OPTIONS.powershell} ghcr.io/openpitkit/officer`,
      ].join("\n"),
    },
  },
  binary: {
    posix: {
      create: [
        "mkdir -p -m 700 ~/.pit-officer",
        "openssl rand -base64 32 > ~/.pit-officer/master.key",
        "chmod 600 ~/.pit-officer/master.key",
      ].join("\n"),
      keep: POSIX_KEEP,
      start:
        'PIT_OFFICER_MASTER_KEY_FILE="$HOME/.pit-officer/master.key" pit-officer serve',
    },
    powershell: {
      create: POWERSHELL_CREATE,
      keep: POWERSHELL_KEEP,
      start: [
        '$env:PIT_OFFICER_MASTER_KEY_FILE = "$HOME\\.pit-officer\\master.key"',
        "pit-officer.exe serve",
      ].join("\n"),
    },
  },
};

/** Names the step texts mention, kept out of the catalogs. */
const DOCKER_VOLUME = "pit-officer-data";
const RUNNING: Record<Shell, string> = {
  posix: "pit-officer serve",
  powershell: "pit-officer.exe serve",
};
const STOP_KEYS = "Ctrl+C";

/** Characters a command block shows on one line before it wraps. */
const WRAP_COLUMNS = 90;

/** Lines a command block needs: each command, a long one wrapped. */
function commandRows(commands: string): number {
  return commands
    .split("\n")
    .reduce((rows, line) => rows + Math.ceil(line.length / WRAP_COLUMNS), 0);
}

/**
 * The system the browser reports, or undefined when its user agent names none
 * of the three; Android and ChromeOS name none of them.
 */
function browserSystem(): System | undefined {
  const agent = navigator.userAgent;
  if (agent.includes("Windows")) {
    return "windows";
  }
  if (agent.includes("Macintosh")) {
    return "mac";
  }
  if (agent.includes("Linux") && !agent.includes("Android")) {
    return "linux";
  }
  return undefined;
}

/** One numbered step: what to do, why, and the commands that do it. */
function GuideStep({
  number,
  title,
  detail,
  warn = false,
  commands,
  children,
}: {
  number: number;
  title: string;
  detail: ReactNode;
  /** Shows the detail as a warning. */
  warn?: boolean;
  commands: string;
  children?: ReactNode;
}) {
  return (
    <li className="grid grid-cols-[1.5rem_minmax(0,1fr)] gap-x-3 border-t border-border pt-3">
      <span
        aria-hidden="true"
        className="flex h-6 w-6 items-center justify-center rounded-full border border-border text-[0.6875rem] text-muted"
      >
        {number}
      </span>
      <div className="min-w-0 flex flex-col gap-y-1.5">
        <p className="text-sm font-medium text-text">{title}</p>
        <p
          className={cn(
            "text-xs",
            warn ? "text-[var(--warn)]" : "text-muted-lt",
          )}
        >
          {detail}
        </p>
        <CopyableSnippet text={commands} rows={commandRows(commands)} />
        {children}
      </div>
    </li>
  );
}

/**
 * How to set a master key in a terminal on this computer: create the key
 * file, keep a copy of the key elsewhere, and start Officer again with it,
 * with copyable commands for Docker or without it, on Windows PowerShell,
 * macOS or Linux. The system starts as the browser reports it; when the
 * browser names none, the commands wait for a choice.
 */
export function MasterKeyGuide() {
  const { t } = useTranslation("service");
  const [deployment, setDeployment] = useState<Deployment>("docker");
  const [system, setSystem] = useState<System | undefined>(browserSystem);
  const code = (text: string) => (
    <code className="font-mono text-text">{text}</code>
  );

  let steps: ReactNode;
  if (system === undefined) {
    steps = (
      <p className="text-xs text-muted-lt">
        {t("masterKeyGuide.chooseSystem")}
      </p>
    );
  } else {
    const shell: Shell = system === "windows" ? "powershell" : "posix";
    const commands = COMMANDS[deployment][shell];
    steps = (
      <ol className="flex flex-col gap-y-3">
        <GuideStep
          number={1}
          title={t("masterKeyGuide.create.title")}
          detail={
            deployment === "docker" && shell === "posix"
              ? t("masterKeyGuide.create.detailDocker")
              : t("masterKeyGuide.create.detail")
          }
          commands={commands.create}
        />
        <GuideStep
          number={2}
          title={t("masterKeyGuide.keep.title")}
          detail={t("masterKeyGuide.keep.detail")}
          warn
          commands={commands.keep}
        />
        <GuideStep
          number={3}
          title={t("masterKeyGuide.start.title")}
          detail={
            deployment === "docker" ? (
              <Trans
                t={t}
                i18nKey="masterKeyGuide.start.detailDocker"
                components={{ volume: code(DOCKER_VOLUME) }}
              />
            ) : (
              <Trans
                t={t}
                i18nKey="masterKeyGuide.start.detailBinary"
                components={{
                  running: code(RUNNING[shell]),
                  keys: <kbd className="font-mono text-text">{STOP_KEYS}</kbd>,
                }}
              />
            )
          }
          commands={commands.start}
        >
          {deployment === "docker" && (
            <>
              <p className="text-xs text-muted-lt">
                <Trans
                  t={t}
                  i18nKey="masterKeyGuide.start.customDocker"
                  components={{ run: code("docker run"), edge: code("edge") }}
                />
              </p>
              <CopyableSnippet
                text={DOCKER_KEY_OPTIONS[shell]}
                rows={commandRows(DOCKER_KEY_OPTIONS[shell])}
              />
            </>
          )}
        </GuideStep>
      </ol>
    );
  }

  return (
    <div className="flex flex-col gap-y-3 rounded-card border border-border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-[0.6875rem] font-medium uppercase tracking-[0.07em] text-muted">
          {t("masterKeyGuide.where")}
        </p>
        <div className="flex flex-wrap gap-2">
          <Segmented
            ariaLabel={t("masterKeyGuide.deployment.label")}
            options={[
              { value: "docker", label: t("masterKeyGuide.deployment.docker") },
              { value: "binary", label: t("masterKeyGuide.deployment.binary") },
            ]}
            value={deployment}
            onChange={(value) => setDeployment(value as Deployment)}
          />
          <Segmented
            ariaLabel={t("masterKeyGuide.system.label")}
            options={[
              { value: "windows", label: t("masterKeyGuide.system.windows") },
              { value: "mac", label: t("masterKeyGuide.system.mac") },
              { value: "linux", label: t("masterKeyGuide.system.linux") },
            ]}
            value={system}
            onChange={(value) => setSystem(value as System)}
          />
        </div>
      </div>
      {steps}
    </div>
  );
}
