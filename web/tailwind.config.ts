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
// Please see https://openpit.dev and the OWNERS file for details.

import type { Config } from "tailwindcss";

// Tokens are driven by CSS custom properties defined in src/index.css so the
// dark/light/system theme switch only has to toggle a class on <html>. The
// palette mirrors the OpenPit website (pit/docs/assets/styles.css).
const config: Config = {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        bg: "var(--bg)",
        surface: "var(--surface)",
        "surface-2": "var(--surface-2)",
        "surface-hover": "var(--surface-hover)",
        "card-hover-bg": "var(--card-hover-bg)",
        border: "var(--border)",
        "border-hover": "var(--border-hover)",
        text: "var(--text)",
        muted: "var(--muted)",
        "muted-lt": "var(--muted-lt)",
        accent: {
          DEFAULT: "var(--accent)",
          2: "var(--accent-2)",
          dim: "var(--accent-dim)",
        },
        ok: "var(--ok)",
        warn: "var(--warn)",
        danger: "var(--danger)",
        ring: "var(--ring)",
        buy: "var(--buy)",
        sell: "var(--sell)",
        long: "var(--long)",
        short: "var(--short)",
        "pnl-pos": "var(--pnl-pos)",
        "pnl-neg": "var(--pnl-neg)",
        "pnl-flat": "var(--pnl-flat)",
        "util-ok": "var(--util-ok)",
        "util-warn": "var(--util-warn)",
        "util-breach": "var(--util-breach)",
        "util-track": "var(--util-track)",
      },
      fontFamily: {
        mono: [
          "'JetBrains Mono'",
          "ui-monospace",
          "'Cascadia Code'",
          "'Fira Code'",
          "monospace",
        ],
      },
      borderRadius: {
        card: "var(--radius-card)",
        badge: "var(--radius-badge)",
      },
      boxShadow: {
        card: "var(--card-shadow)",
      },
      keyframes: {
        "fade-in": {
          from: { opacity: "0", transform: "translateY(4px)" },
          to: { opacity: "1", transform: "translateY(0)" },
        },
      },
      animation: {
        "fade-in": "fade-in 0.18s ease",
      },
    },
  },
  plugins: [],
};

export default config;
