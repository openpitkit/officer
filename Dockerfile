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

# Build context must be the workspace root (parent of officer/ and pit/): the
# replace directive in officer/go.mod points to ../pit/bindings/go.

# Stage 1: build the dashboard SPA.
FROM node:20-alpine AS frontend

WORKDIR /app/web

# Install dependencies in their own layer for caching.
COPY officer/web/package.json ./
RUN npm install

COPY officer/web/ ./
RUN npm run build

# Stage 2: build the native runtime library by checking out and compiling pit.
# Used only when DYLIB_PATH is empty; otherwise overridden in the Go stage.
FROM rust:1.87-slim AS dylib

ARG PIT_REF=main

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /pit

RUN git clone --branch "${PIT_REF}" --depth 1 \
    https://github.com/openpitkit/pit.git . \
    && cargo build -p openpit-ffi --release --locked \
    && cp target/release/libopenpit_ffi.so /libopenpit_ffi.so

# Stage 3: build the pit-officer binary (cgo required by the openpit binding).
FROM golang:1.23-bookworm AS gobuild

# Path to a pre-built dylib relative to the build context. Empty uses the
# library compiled in the dylib stage.
ARG DYLIB_PATH=

RUN apt-get update && apt-get install -y --no-install-recommends \
    gcc \
    libc6-dev \
    && rm -rf /var/lib/apt/lists/*

# Replicate officer/go.mod's sibling layout (replace -> ../pit/bindings/go).
COPY pit/bindings/go/ /workspace/pit/bindings/go/

# go.mod/go.sum first for a cacheable download layer.
COPY officer/go.mod officer/go.sum* /workspace/officer/

WORKDIR /workspace/officer

RUN go mod download

COPY officer/ /workspace/officer/

COPY --from=frontend /app/web/dist /workspace/officer/web/dist

RUN mkdir -p /runtime

COPY --from=dylib /libopenpit_ffi.so /runtime/libopenpit_ffi.so

# Override with an injected dylib when DYLIB_PATH is set (resolved relative to
# the officer module root); a no-op when empty.
RUN if [ -n "${DYLIB_PATH}" ]; then \
    cp "/workspace/officer/${DYLIB_PATH}" /runtime/libopenpit_ffi.so; \
    fi

# -ldflags "-s -w" strips debug symbols for a smaller binary.
RUN CGO_ENABLED=1 \
    OPENPIT_RUNTIME_LIBRARY_PATH=/runtime/libopenpit_ffi.so \
    go build \
    -ldflags="-s -w" \
    -o /pit-officer \
    ./cmd/pit-officer

# Stage 4: runtime image.
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

COPY --from=gobuild /runtime/libopenpit_ffi.so /app/lib/libopenpit_ffi.so

COPY --from=gobuild /pit-officer /app/pit-officer

COPY --from=frontend /app/web/dist /app/web/dist

# Pin the binding to the pre-extracted library so it skips embedded extraction.
ENV OPENPIT_RUNTIME_LIBRARY_PATH=/app/lib/libopenpit_ffi.so

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
