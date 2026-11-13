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

import type { Policy } from "@/api/vocabulary";
import { VideoLink } from "@/components/video/VideoLink";
import type { VideoKey } from "@/components/video/videos";

const POLICY_VIDEOS: Partial<Record<Policy, VideoKey>> = {
  rate_limit: "rate",
  order_size_limit: "size",
  spot_funds_pnl_bounds_kill_switch: "pnl",
  spot_funds_unrealized_pnl_bounds_kill_switch: "pnl",
  spot_funds_total_pnl_bounds_kill_switch: "pnl",
};

/** Shows a related guide with the same language resolution as the Policies page. */
export function PolicyVideoLink({ policy }: { policy: Policy }) {
  const video = POLICY_VIDEOS[policy];
  return video === undefined ? null : <VideoLink video={video} />;
}
