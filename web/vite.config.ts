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

import { fileURLToPath, URL } from "node:url";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";

import { createAppRouteManifest } from "./src/appRoutes";

const appRouteManifestFile = "app-route-manifest.json";

function appRouteManifestPlugin(): Plugin {
  const source = `${JSON.stringify(createAppRouteManifest(), null, 2)}\n`;

  return {
    name: "app-route-manifest",
    configureServer(server) {
      server.middlewares.use((request, response, next) => {
        if (
          request.method !== "GET" ||
          request.url?.split("?", 1)[0] !== `/${appRouteManifestFile}`
        ) {
          next();
          return;
        }
        response.statusCode = 200;
        response.setHeader("Content-Type", "application/json; charset=utf-8");
        response.end(source);
      });
    },
    generateBundle() {
      this.emitFile({
        type: "asset",
        fileName: appRouteManifestFile,
        source,
      });
    },
  };
}

// The dashboard is embedded into the Go binary (go:embed web/dist) and
// served from the site root by the Pit Officer HTTP layer, so base must
// be "/" and the build output must land in web/dist.
export default defineConfig({
  base: "/",
  plugins: [react(), appRouteManifestPlugin()],
  resolve: {
    alias: {
      "@openpit/officer-web": fileURLToPath(
        new URL("./src/index.ts", import.meta.url),
      ),
      "@": fileURLToPath(new URL("./src", import.meta.url)),
    },
  },
  build: {
    outDir: "dist",
    // Keep the known operator bundle below an explicit size budget.
    chunkSizeWarningLimit: 800,
    // npm run prepare:dist cleans generated files while preserving the
    // committed embed placeholder required by go:embed on fresh clones.
    emptyOutDir: false,
    sourcemap: false,
    rollupOptions: {
      output: {
        manualChunks(id) {
          if (!id.includes("node_modules")) return undefined;
          if (id.includes("@radix-ui")) return "radix";
          if (id.includes("i18next") || id.includes("react-i18next"))
            return "i18n";
          return "vendor";
        },
      },
    },
  },
  server: {
    port: 5173,
    // Local dev proxy so the SPA can call the Go backend's API surface while
    // running under Vite. Run the backend pinned to this port in dev:
    // `pit-officer serve -http-addr 127.0.0.1:8787` (serve defaults to a free
    // port). The embedded production build is same-origin and ignores this.
    // `/app/api/v1` is the panel surface consumed by the SPA. `/api/v1` and
    // `/api` stay proxied for direct control-plane probes during development.
    proxy: {
      "/app/api/v1": {
        target: "http://127.0.0.1:8787",
        changeOrigin: true,
      },
      "/api/v1": {
        target: "http://127.0.0.1:8787",
        changeOrigin: true,
      },
      "/api": {
        target: "http://127.0.0.1:8787",
        changeOrigin: true,
      },
    },
  },
});
