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

import { Button } from "@/components/ui/button";
import { videoUrl, type VideoKey } from "@/components/video/videos";
import { YouTubeLogo } from "@/components/video/YouTubeLogo";
import { cn } from "@/lib/utils";

const SIZES = {
  sm: "w-[var(--dens-button-sm-h)]",
  field: "h-[var(--dens-field-h)] w-[var(--dens-field-h)]",
  link: "h-4 w-6",
} as const;

/**
 * One video guide as a small YouTube glyph, placed at the end of the row of the
 * control it explains and as tall as that row's controls: `sm` beside small
 * buttons, tabs and segmented choices, `field` beside an input, `link` in a row
 * of text links. Never in the page top bar: a guide belongs to the control,
 * not to the screen.
 */
export function VideoGuideButton({
  video,
  size = "sm",
  className,
}: {
  video: VideoKey;
  size?: keyof typeof SIZES;
  className?: string;
}) {
  const { t, i18n } = useTranslation();
  const label = t("videoGuide.button", {
    name: t(`videoGuide.video.${video}`),
  });
  return (
    <Button
      asChild
      variant="ghost"
      size="sm"
      className={cn("shrink-0 px-0", SIZES[size], className)}
    >
      <a
        href={videoUrl(video, i18n.language)}
        target="_blank"
        rel="noopener noreferrer"
        aria-label={label}
        title={label}
      >
        <YouTubeLogo />
      </a>
    </Button>
  );
}
