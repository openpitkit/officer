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

import type { ComponentProps } from "react";

import type { Policy } from "@/api/vocabulary";
import { VideoGuideButton } from "@/components/video/VideoGuideButton";
import type { VideoKey } from "@/components/video/videos";

// A Map, as a stored limit's policy comes from the server as a string and a
// plain object would answer for prototype keys such as `toString`.
const POLICY_VIDEOS = new Map<Policy, VideoKey>([
  ["order_size_limit", "size"],
  ["rate_limit", "rate"],
  ["spot_funds_pnl_bounds_kill_switch", "pnl"],
  ["spot_funds_unrealized_pnl_bounds_kill_switch", "pnl"],
  ["spot_funds_total_pnl_bounds_kill_switch", "pnl"],
  ["spot_funds_limit_mode", "whyBalances"],
]);

/** The guide to `policy` as a small YouTube glyph; nothing when it has none. */
export function PolicyVideoButton({
  policy,
  size,
}: {
  policy: Policy;
  size?: ComponentProps<typeof VideoGuideButton>["size"];
}) {
  const video = POLICY_VIDEOS.get(policy);
  return video === undefined ? null : (
    <VideoGuideButton video={video} size={size} />
  );
}
