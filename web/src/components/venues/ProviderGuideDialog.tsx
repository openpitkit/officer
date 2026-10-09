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

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { ProviderTile, type VenueProviderOption } from "./ProviderTile";

/** Controlled dialog state, copy, provider entries, and picker callbacks. */
export interface ProviderGuideDialogProps {
  open: boolean;
  title: string;
  description: string;
  providers: VenueProviderOption[];
  onSelect: (provider: VenueProviderOption) => void;
  onOpenChange: (open: boolean) => void;
}

/** Displays a controlled provider picker using caller-resolved copy. */
export function ProviderGuideDialog({
  open,
  title,
  description,
  providers,
  onSelect,
  onOpenChange,
}: ProviderGuideDialogProps) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[90vh] max-w-[40rem] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <div className="grid gap-3 sm:grid-cols-2">
          {providers.map((provider) => (
            <ProviderTile
              key={provider.type}
              provider={provider}
              onSelect={onSelect}
            />
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
