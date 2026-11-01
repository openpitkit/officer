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

import { ExternalLinkCard } from "@/components/ExternalLinkCard";
import { videoUrl, type VideoKey } from "@/components/video/videos";

/**
 * A panel video guide in the panel language, used by the wizard and section
 * plaques. Nothing is loaded from YouTube until the link is opened.
 */
export function VideoLink({ video }: { video: VideoKey }) {
  const { t, i18n } = useTranslation();
  return (
    <ExternalLinkCard
      href={videoUrl(video, i18n.language)}
      label={t(`videoGuide.video.${video}`)}
      icon={
        <svg
          aria-hidden="true"
          viewBox="0 0 28.57 20"
          className="mr-2 inline-block h-3.5 w-5 align-text-bottom"
        >
          <path
            fill="#FF0000"
            d="M27.9727 3.12324C27.6435 1.89323 26.6768 0.926623 25.4468 0.597366C23.2197 2.24288e-07 14.285 0 14.285 0C14.285 0 5.35042 2.24288e-07 3.12323 0.597366C1.89323 0.926623 0.926623 1.89323 0.597366 3.12324C2.24288e-07 5.35042 0 10 0 10C0 10 2.24288e-07 14.6496 0.597366 16.8768C0.926623 18.1068 1.89323 19.0734 3.12323 19.4026C5.35042 20 14.285 20 14.285 20C14.285 20 23.2197 20 25.4468 19.4026C26.6768 19.0734 27.6435 18.1068 27.9727 16.8768C28.5701 14.6496 28.5701 10 28.5701 10C28.5701 10 28.5677 5.35042 27.9727 3.12324Z"
          />
          <path
            fill="white"
            d="M11.4253 14.2854L18.8477 10.0004L11.4253 5.71533V14.2854Z"
          />
        </svg>
      }
    />
  );
}
