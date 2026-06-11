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

# Development shortcuts for the pit-officer module.

workspace_root := justfile_directory() / ".."
pit_dir        := workspace_root / "pit"

dylib_path_linux  := pit_dir / "target/release/libopenpit_ffi.so"
dylib_path_darwin := pit_dir / "target/release/libopenpit_ffi.dylib"

# Runtime library path; CI overrides it with the downloaded artifact.
openpit_runtime := env_var_or_default("OPENPIT_RUNTIME_LIBRARY_PATH", if os() == "macos" { dylib_path_darwin } else { dylib_path_linux })

# Build the native OpenPit runtime library from the sibling pit repo.
dylib:
    cargo build -p openpit-ffi --release --locked \
        --manifest-path {{ pit_dir }}/Cargo.toml

# Regenerate go.sum and tidy indirect dependencies.
tidy:
    CGO_ENABLED=1 \
    OPENPIT_RUNTIME_LIBRARY_PATH={{ openpit_runtime }} \
    go mod tidy

# Build the pit-officer binary.
build: frontend-build
    CGO_ENABLED=1 \
    OPENPIT_RUNTIME_LIBRARY_PATH={{ openpit_runtime }} \
    go build -o pit-officer ./cmd/pit-officer

# Build all Go packages (no frontend; uses the committed web/dist placeholder).
build-go:
    CGO_ENABLED=1 \
    OPENPIT_RUNTIME_LIBRARY_PATH={{ openpit_runtime }} \
    go build ./...

# Run go vet across all packages.
vet:
    CGO_ENABLED=1 \
    OPENPIT_RUNTIME_LIBRARY_PATH={{ openpit_runtime }} \
    go vet ./...

# Run all tests with the race detector.
test:
    CGO_ENABLED=1 \
    OPENPIT_RUNTIME_LIBRARY_PATH={{ openpit_runtime }} \
    go test -race -count=1 ./...

# Run pit-officer in local stdio MCP mode.
run-mcp: build
    OPENPIT_RUNTIME_LIBRARY_PATH={{ openpit_runtime }} \
    ./pit-officer mcp

# Run pit-officer in serve mode (MCP over HTTP + dashboard).
run-serve: build
    OPENPIT_RUNTIME_LIBRARY_PATH={{ openpit_runtime }} \
    ./pit-officer serve

# Print the running serve URL and open it (no rebuild; serve must be up).
dashboard:
    ./pit-officer dashboard

# Install frontend deps and build the SPA into web/dist/.
frontend-build:
    cd web && npm install && npm run build

# Install frontend dependencies only (no build).
frontend-install:
    cd web && npm install

# Build the Docker image (Strategy A: dylib built inside Docker).
docker-build PIT_REF="main":
    docker build \
        -f {{ justfile_directory() }}/Dockerfile \
        --build-arg PIT_REF={{ PIT_REF }} \
        -t pit-officer:local \
        {{ workspace_root }}

# Build the Docker image with a locally pre-built dylib (Strategy B).
docker-build-local: dylib
    #!/usr/bin/env bash
    set -euo pipefail
    # Docker images are Linux; a macOS dylib cannot run inside the container.
    case "$(uname -s)" in
      Darwin) lib="{{ pit_dir }}/target/release/libopenpit_ffi.dylib" ;;
      Linux)  lib="{{ pit_dir }}/target/release/libopenpit_ffi.so" ;;
      *) echo "unsupported OS" >&2; exit 1 ;;
    esac
    cp "$lib" "{{ justfile_directory() }}/libopenpit_ffi.so"
    trap 'rm -f "{{ justfile_directory() }}/libopenpit_ffi.so"' EXIT
    docker build \
        -f {{ justfile_directory() }}/Dockerfile \
        --build-arg DYLIB_PATH=libopenpit_ffi.so \
        -t pit-officer:local \
        {{ workspace_root }}

# Seed a running Officer instance with demo accounts, balances, orders, and trades.
seed:
    python3 {{ justfile_directory() }}/tools/seed/seed.py
