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

import { Play } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Notice } from "@/components/Notice";
import { VideoLink } from "@/components/video/VideoLink";
import type { VideoKey } from "@/components/video/videos";

/** Guides for a section that has no operator-created records yet. */
export function VideoGuideNotice({ videos }: { videos: VideoKey[] }) {
  const { t } = useTranslation("common");
  return (
    <Notice tone="info" icon={Play} title={t("videoGuide.title")}>
      {videos.map((video) => (
        <VideoLink key={video} video={video} />
      ))}
    </Notice>
  );
}
