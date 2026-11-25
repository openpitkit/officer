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

/**
 * Panel video guides on the OpenPit channel, for the wizard, the section
 * plaques and the guide buttons beside the controls they explain. Ids come from
 * the Officer video registry, the only list of published videos; each entry
 * names its registry row.
 */
export const VIDEOS = {
  /** "What is Officer" (`what-is-officer`). */
  intro: {
    cuts: {
      en: { id: "AILRFZbGxJs" },
      ru: { id: "JxCHImpsmc4" },
      "zh-CN": { id: "zMAwfEEA2oA" },
      es: { id: "-yPBdRiOjs0" },
      "pt-BR": { id: "rQ2xRUP4mtQ" },
    },
    captions: [],
  },
  /**
   * "Run Officer 0.1" (`quickstart-0-1`), from its chapter on connecting an
   * agent over MCP. The Spanish cut is left out until its chapter start is
   * known: the English chapter with Spanish captions lands on the right scene.
   */
  mcp: {
    cuts: {
      en: { id: "zq7a9xc0GD0", start: 121 },
      ru: { id: "n_AWcXqedV0", start: 136 },
    },
    captions: ["zh-CN", "es", "pt-BR"],
  },
  /** "Connect market data" (`connect-prices`). */
  prices: {
    cuts: {
      en: { id: "7nM26GUgtes" },
      ru: { id: "8iZq4OaV0TY" },
      es: { id: "ZW2b4hCLiXc" },
    },
    captions: ["zh-CN", "pt-BR"],
  },
  /** "Why Officer needs prices" (`why-prices`). */
  whyPrices: {
    cuts: { en: { id: "tsv2ABKFdSk" } },
    captions: ["ru", "zh-CN", "es", "pt-BR"],
  },
  /** "Where your data lives" (`where-data-lives`). */
  whereDataLives: {
    cuts: { en: { id: "4q_1yB2SQqA" } },
    captions: [],
  },
  /** "Can an agent bypass the limits" (`can-agent-bypass`). */
  canAgentBypass: {
    cuts: { en: { id: "jxoP79byyGc" } },
    captions: [],
  },
  /** "Order size limit" (`order-size-limit`). */
  size: {
    cuts: {
      en: { id: "aXXWLLw3Jm0" },
      ru: { id: "HmK7fCvACjE" },
      es: { id: "D_6FDDzgSK8" },
    },
    captions: ["zh-CN", "pt-BR"],
  },
  /** "Order rate limit" (`order-rate-limit`). */
  rate: {
    cuts: {
      en: { id: "BpQoS5UUEsU" },
      ru: { id: "tILQ7UjSy7Q" },
      es: { id: "nFaBrgyjenY" },
    },
    captions: ["zh-CN", "pt-BR"],
  },
  /** "Stop on profit and loss" (`pnl-kill-switch`). */
  pnl: {
    cuts: {
      en: { id: "kkcir-NpZBA" },
      ru: { id: "XVVI18V3dqk" },
      es: { id: "nBQguXs8lic" },
    },
    captions: ["zh-CN", "pt-BR"],
  },
  /** "Stop everything right now" (`stop-now`). */
  stop: {
    cuts: {
      en: { id: "ateQ-i5k-o0" },
      ru: { id: "WcZLKGHFFGQ" },
      es: { id: "PQrGg_cDYX0" },
    },
    captions: ["zh-CN", "pt-BR"],
  },
  /** "Order review" (`order-review`). */
  orderReview: {
    cuts: { en: { id: "qE-WXp3Sz78" } },
    captions: ["ru", "zh-CN", "es", "pt-BR"],
  },
  /** "Test connection" (`test-connection`). */
  testConnection: {
    cuts: { en: { id: "2ke1H6TuN0E" } },
    captions: ["ru", "zh-CN", "es", "pt-BR"],
  },
  /** "New trading connections" (`new-trading-connections`). */
  newTradingConnections: {
    cuts: { en: { id: "d4NQ3fGXyww" } },
    captions: ["ru", "zh-CN", "es", "pt-BR"],
  },
  /** "Paper trading first" (`paper-trading-first`). */
  paperTradingFirst: {
    cuts: {
      en: { id: "LgDSHpEeHpo" },
      ru: { id: "QpGpnPY63jM" },
      "zh-CN": { id: "FBkilwQH90w" },
      es: { id: "u_cZfqSJBqE" },
      "pt-BR": { id: "VvRa4EBL7jU" },
    },
    captions: [],
  },
  /** "Alpaca and Claude through Officer" (`alpaca-mcp`). */
  alpacaMcp: {
    cuts: { en: { id: "y6_4DJy2tUo" } },
    captions: ["ru", "zh-CN", "es", "pt-BR"],
  },
  /** "A Codex trading bot with limits" (`codex-trading`). */
  codexTrading: {
    cuts: { en: { id: "pEUH5cbitBo" } },
    captions: ["ru", "zh-CN", "es", "pt-BR"],
  },
  /** "A Cursor trading bot with limits" (`cursor-trading`). */
  cursorTrading: {
    cuts: { en: { id: "eqd5iH2yaiA" } },
    captions: ["ru", "zh-CN", "es", "pt-BR"],
  },
  /** "Why Officer needs balances" (`why-balances`). */
  whyBalances: {
    cuts: {
      en: { id: "na6qjWOZodc" },
      ru: { id: "qOPaL-VOyAM" },
      es: { id: "vYG_beCi1Go" },
    },
    captions: ["zh-CN", "pt-BR"],
  },
  /** "Balances and cash movements" (`balances`). */
  balances: {
    cuts: {
      en: { id: "6Dv9wQnFnRM" },
      ru: { id: "BRz6M3O_3sU" },
      es: { id: "6NopWBreP7k" },
    },
    captions: ["zh-CN", "pt-BR"],
  },
  /** "Account groups" (`account-groups`). */
  groups: {
    cuts: {
      en: { id: "_zRQy5L4Luw" },
      ru: { id: "p885xVaJQkU" },
      es: { id: "GmLHCaiaKGo" },
    },
    captions: ["zh-CN", "pt-BR"],
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
