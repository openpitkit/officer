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

/** One published cut of a video: its YouTube id and where the link starts. */
type Cut = {
  readonly id: string;
  /** Seconds from the start; absent when the link opens at the beginning. */
  readonly start?: number;
};

type Video = {
  /** The cut per panel language that has its own; English always has one. */
  readonly cuts: { readonly en: Cut } & Readonly<Partial<Record<string, Cut>>>;
  /** The panel languages the English cut carries published captions in. */
  readonly captions: readonly string[];
};

/** The wizard's videos on the OpenPit channel. */
export const VIDEOS = {
  /** "What is Pit Officer". */
  intro: {
    cuts: {
      en: { id: "v34IdYhH5so" },
      ru: { id: "F1ymuREbu4o" },
      "zh-CN": { id: "JxojlbYnGIE" },
      es: { id: "Oq13uf44pEc" },
      "pt-BR": { id: "xgUOyBwjsgg" },
    },
    captions: [],
  },
  /** "How to install Pit Officer", from its chapter on connecting over MCP. */
  mcp: {
    cuts: {
      en: { id: "zq7a9xc0GD0", start: 121 },
      ru: { id: "n_AWcXqedV0", start: 136 },
    },
    captions: ["zh-CN", "es", "pt-BR"],
  },
} as const satisfies Record<string, Video>;

export type VideoKey = keyof typeof VIDEOS;

/**
 * The YouTube link for `language`: that language's own cut when it has one;
 * otherwise the English cut with captions on in `language` when it carries
 * them; otherwise the English cut as it is.
 */
export function videoUrl(key: VideoKey, language: string): string {
  const video: Video = VIDEOS[key];
  const own = video.cuts[language];
  const cut = own ?? video.cuts.en;
  const url = new URL("https://www.youtube.com/watch");
  url.searchParams.set("v", cut.id);
  if (cut.start !== undefined) {
    url.searchParams.set("t", `${cut.start}s`);
  }
  if (own === undefined && video.captions.includes(language)) {
    // The watch page honours the embed player's caption parameters.
    url.searchParams.set("cc_load_policy", "1");
    url.searchParams.set("cc_lang_pref", language);
  }
  return url.href;
}
