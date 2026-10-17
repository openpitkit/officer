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

import { X } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import { useAccountMode } from "@/api/accountMode";
import { AccountModePanel } from "@/components/AccountModeChoice";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { useOfficerApi } from "@/framework";

/**
 * Asks for the account mode, as on a first start with no mode stored. Calls
 * `onDone` once it closes, chosen or not, unless Officer was asked to restart
 * to apply the choice: the page reloads when Officer is back instead.
 */
export function AccountModeDialog({ onDone }: { onDone: () => void }) {
  const { t } = useTranslation();
  const { restartService } = useOfficerApi();
  const poll = useAccountMode();
  const [open, setOpen] = useState(true);
  const [restarting, setRestarting] = useState(false);

  const restart = async () => {
    await restartService();
    setRestarting(true);
  };

  const close = () => {
    setOpen(false);
    if (!restarting) {
      onDone();
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) {
          close();
        }
      }}
    >
      <DialogContent hideClose>
        <div className="flex items-center justify-end gap-2">
          <LanguageSwitch />
          <ThemeSwitch />
          <DialogClose asChild>
            <Button
              type="button"
              variant="ghost"
              size="icon"
              aria-label={t("actions.close")}
            >
              <X />
            </Button>
          </DialogClose>
        </div>
        <DialogHeader>
          <DialogTitle>{t("accountModeDialog.title")}</DialogTitle>
          <DialogDescription>
            {t("accountModeDialog.description")}
          </DialogDescription>
        </DialogHeader>
        <AccountModePanel poll={poll} restart={restart} onSaved={close} />
      </DialogContent>
    </Dialog>
  );
}
