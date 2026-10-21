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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { MasterKeyGuide } from "@/components/MasterKeyGuide";
import i18n from "@/i18n";

// The access design's exact instructions, one command per line.
const POSIX_DOCKER_CREATE = `mkdir -p -m 700 ~/.pit-officer
openssl rand -base64 32 > ~/.pit-officer/master.key
chmod 644 ~/.pit-officer/master.key`;
const POSIX_BINARY_CREATE = `mkdir -p -m 700 ~/.pit-officer
openssl rand -base64 32 > ~/.pit-officer/master.key
chmod 600 ~/.pit-officer/master.key`;
const POSIX_KEEP = "cat ~/.pit-officer/master.key";
const POSIX_DOCKER_START = `docker stop pit-officer
docker rm pit-officer
docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data -v "$HOME/.pit-officer/master.key:/run/secrets/pit-officer-master-key:ro" -e PIT_OFFICER_MASTER_KEY_FILE=/run/secrets/pit-officer-master-key ghcr.io/openpitkit/officer`;
const POSIX_BINARY_START = `PIT_OFFICER_MASTER_KEY_FILE="$HOME/.pit-officer/master.key" pit-officer serve`;
const POWERSHELL_CREATE = String.raw`New-Item -ItemType Directory -Force "$HOME\.pit-officer" | Out-Null
$key = New-Object byte[] 32
[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($key)
[IO.File]::WriteAllText("$HOME\.pit-officer\master.key", [Convert]::ToBase64String($key))`;
const POWERSHELL_KEEP = String.raw`Get-Content "$HOME\.pit-officer\master.key"`;
const POWERSHELL_DOCKER_START = String.raw`docker stop pit-officer
docker rm pit-officer
docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data -v "$HOME\.pit-officer\master.key:/run/secrets/pit-officer-master-key:ro" -e PIT_OFFICER_MASTER_KEY_FILE=/run/secrets/pit-officer-master-key ghcr.io/openpitkit/officer`;
const POWERSHELL_BINARY_START = String.raw`$env:PIT_OFFICER_MASTER_KEY_FILE = "$HOME\.pit-officer\master.key"
pit-officer.exe serve`;

const MAC_AGENT =
  "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15";
const WINDOWS_AGENT =
  "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36";
const LINUX_AGENT =
  "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0";
const ANDROID_AGENT =
  "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36";

function renderGuide(userAgent?: string) {
  if (userAgent !== undefined) {
    vi.spyOn(window.navigator, "userAgent", "get").mockReturnValue(userAgent);
  }
  return render(
    <I18nextProvider i18n={i18n}>
      <MasterKeyGuide />
    </I18nextProvider>,
  );
}

function steps() {
  return screen.getAllByRole("listitem");
}

function commandsOf(step: HTMLElement) {
  return within(step).getByRole("textbox", { name: "Copyable snippet" });
}

function choice(group: string, name: string) {
  return within(screen.getByRole("group", { name: group })).getByRole(
    "button",
    { name },
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("MasterKeyGuide", () => {
  it.each([
    ["Docker", "macOS", POSIX_DOCKER_CREATE, POSIX_KEEP, POSIX_DOCKER_START],
    ["Docker", "Linux", POSIX_DOCKER_CREATE, POSIX_KEEP, POSIX_DOCKER_START],
    [
      "Docker",
      "Windows PowerShell",
      POWERSHELL_CREATE,
      POWERSHELL_KEEP,
      POWERSHELL_DOCKER_START,
    ],
    [
      "Without Docker",
      "macOS",
      POSIX_BINARY_CREATE,
      POSIX_KEEP,
      POSIX_BINARY_START,
    ],
    [
      "Without Docker",
      "Linux",
      POSIX_BINARY_CREATE,
      POSIX_KEEP,
      POSIX_BINARY_START,
    ],
    [
      "Without Docker",
      "Windows PowerShell",
      POWERSHELL_CREATE,
      POWERSHELL_KEEP,
      POWERSHELL_BINARY_START,
    ],
  ])(
    "shows and copies the exact commands for %s on %s",
    async (deployment, system, create, keep, start) => {
      const user = userEvent.setup();
      const { container } = renderGuide();

      await user.click(choice("How Officer runs", deployment));
      await user.click(choice("System", system));
      expect(choice("How Officer runs", deployment)).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(choice("System", system)).toHaveAttribute("aria-pressed", "true");

      const expected = [create, keep, start];
      const shown = steps();
      expect(shown).toHaveLength(3);
      for (const [index, step] of shown.entries()) {
        expect(commandsOf(step)).toHaveValue(expected[index]);
        await user.click(within(step).getByRole("button", { name: "Copy" }));
        expect(await navigator.clipboard.readText()).toBe(expected[index]);
      }
      // The key itself never rides in an environment variable.
      for (const step of shown) {
        expect((commandsOf(step) as HTMLTextAreaElement).value).not.toContain(
          "PIT_OFFICER_MASTER_KEY=",
        );
      }
      expect(container.textContent).not.toContain("PIT_OFFICER_MASTER_KEY=");
    },
  );

  it("names the three steps and says what each one does", async () => {
    const user = userEvent.setup();
    renderGuide(MAC_AGENT);

    const [create, keep, start] = steps();
    expect(within(create).getByText("Create the key file")).toBeInTheDocument();
    expect(
      within(create).getByText(
        "32 random bytes, in a folder only you can open. The file itself stays readable, so Officer's container can read it.",
      ),
    ).toBeInTheDocument();
    expect(
      within(keep).getByText("Keep a copy of the key somewhere else"),
    ).toBeInTheDocument();
    expect(
      within(keep).getByText(
        "Put it into your password manager, or print it and store it safely. Lose the key and Officer never opens its database again: there is no recovery.",
      ),
    ).toBeInTheDocument();
    expect(
      within(start).getByText("Start Officer again with the key"),
    ).toBeInTheDocument();
    expect(start).toHaveTextContent(
      "This replaces the container. Your data stays in the volume pit-officer-data.",
    );

    await user.click(choice("How Officer runs", "Without Docker"));
    expect(
      within(steps()[0]).getByText(
        "32 random bytes, in a folder only you can open.",
      ),
    ).toBeInTheDocument();
    expect(steps()[2]).toHaveTextContent(
      "Stop the running pit-officer serve with Ctrl+C, then start it like this, with the options you used before.",
    );

    await user.click(choice("System", "Windows PowerShell"));
    expect(steps()[2]).toHaveTextContent(
      "Stop the running pit-officer.exe serve with Ctrl+C, then start it like this, with the options you used before.",
    );
  });

  it.each([
    [MAC_AGENT, "macOS", POSIX_DOCKER_CREATE],
    [WINDOWS_AGENT, "Windows PowerShell", POWERSHELL_CREATE],
    [LINUX_AGENT, "Linux", POSIX_DOCKER_CREATE],
  ])(
    "starts on the system the browser reports, under Docker",
    (userAgent, system, create) => {
      renderGuide(userAgent);

      expect(choice("System", system)).toHaveAttribute("aria-pressed", "true");
      expect(choice("How Officer runs", "Docker")).toHaveAttribute(
        "aria-pressed",
        "true",
      );
      expect(commandsOf(steps()[0])).toHaveValue(create);
    },
  );

  it("chooses no system and shows no commands while the browser names none", async () => {
    const user = userEvent.setup();
    renderGuide(ANDROID_AGENT);

    for (const system of ["Windows PowerShell", "macOS", "Linux"]) {
      expect(choice("System", system)).toHaveAttribute("aria-pressed", "false");
    }
    expect(screen.queryByRole("listitem")).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "Choose the system of this computer to see its commands.",
      ),
    ).toBeInTheDocument();

    await user.click(choice("System", "Linux"));
    expect(commandsOf(steps()[0])).toHaveValue(POSIX_DOCKER_CREATE);
  });

  it("says nothing about changing or removing the key", () => {
    const { container } = renderGuide(MAC_AGENT);

    expect(container.textContent).not.toMatch(/change|remove|rotate/i);
  });
});
