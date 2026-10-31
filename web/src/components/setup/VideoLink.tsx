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
import { videoUrl, type VideoKey } from "@/components/setup/videos";

/**
 * A link to one of the wizard's videos in the panel language. It is a plain
 * link: nothing is loaded from YouTube until it is opened.
 */
export function VideoLink({ video }: { video: VideoKey }) {
  const { t, i18n } = useTranslation("setup");
  return (
    <ExternalLinkCard
      href={videoUrl(video, i18n.language)}
      label={t(`video.${video}`)}
    />
  );
}
