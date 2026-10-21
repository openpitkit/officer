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

import { useTranslation } from "react-i18next";

import { BrandMark } from "@/components/BrandMark";

/**
 * The brand mark beside the product name and the control-plane label. It
 * renders the two as siblings: the parent lays them out in a row.
 */
export function BrandLockup() {
  const { t } = useTranslation();
  return (
    <>
      <BrandMark className="h-[var(--dens-brand-logo)] w-[var(--dens-brand-logo)]" />
      <div className="min-w-0 leading-tight">
        <div className="text-[length:var(--dens-brand-title-fz)] font-bold tracking-tight text-text">
          {t("brand.name")}
        </div>
        <div className="text-[length:var(--dens-brand-label-fz)] uppercase tracking-[0.12em] text-muted">
          {t("brand.controlPlane")}
        </div>
      </div>
    </>
  );
}
