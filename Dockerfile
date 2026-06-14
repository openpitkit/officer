# Copyright The Pit Project Owners. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Please see https://openpit.dev and the OWNERS file for details.

# Build context is the officer module root.

# Stage 1: build the dashboard SPA.
FROM node:20-alpine AS frontend

WORKDIR /app/web

# Install dependencies in their own layer for caching.
COPY web/package.json web/package-lock.json ./
RUN npm install

COPY web/ ./
RUN npm run build

# Stage 2: build the pit-officer binary (cgo required by the openpit binding).
FROM golang:1.25-bookworm AS gobuild

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# go.mod/go.sum first for a cacheable download layer.
COPY go.mod go.sum* /workspace/officer/

WORKDIR /workspace/officer

RUN go mod download

COPY . /workspace/officer/

COPY --from=frontend /app/web/dist /workspace/officer/web/dist

# -ldflags "-s -w" strips debug symbols for a smaller binary.
RUN CGO_ENABLED=1 go build \
    -ldflags="-s -w" \
    -o /pit-officer \
    ./cmd/pit-officer

# Stage 3: runtime image.
FROM debian:bookworm-slim AS runtime

# C runtime required by the openpit binding (libgcc / glibc).
RUN apt-get update && apt-get install -y --no-install-recommends \
    libgcc-s1 \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

RUN useradd -r -s /sbin/nologin -u 1001 officer

# Own /data before dropping privileges, else SQLite cannot create the db file.
RUN mkdir -p /data && chown officer:officer /data

WORKDIR /app

COPY --from=gobuild /pit-officer /app/pit-officer

COPY --from=frontend /app/web/dist /app/web/dist

# The default 127.0.0.1 is unreachable across the container boundary; bind all
# interfaces and rely on Docker port-mapping. Front with a TLS reverse proxy.
ENV PIT_OFFICER_HTTP_ADDR=0.0.0.0:8787

VOLUME ["/data"]
ENV PIT_OFFICER_SQLITE_PATH=/data/pit-officer.db

# Headless container: never attempt to launch a browser on serve start.
ENV PIT_OFFICER_OPEN_BROWSER=false

EXPOSE 8787

USER officer

ENTRYPOINT ["/app/pit-officer"]
CMD ["serve"]
