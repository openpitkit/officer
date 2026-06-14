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
#
# Default recipes build against the published OpenPit module pinned in go.mod.
# Recipes ending in -dev build against a local Pit checkout without modifying
# go.mod or go.sum.

native_runtime_name := if os() == "macos" { "libopenpit_ffi.dylib" } else { "libopenpit_ffi.so" }
go_cache := env_var_or_default("GOCACHE", "/tmp/pit-officer-go-build-cache")
golangci_lint_cache := env_var_or_default("GOLANGCI_LINT_CACHE", "/tmp/pit-officer-golangci-lint-cache")
go_packages := ". ./cmd/... ./internal/..."

# Regenerate go.sum and tidy indirect dependencies.
tidy:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go mod tidy

# Verify go.mod and go.sum are already tidy.
tidy-check:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go mod tidy
    git diff --exit-code -- go.mod go.sum

# Build the pit-officer binary.
build: frontend-build
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go build -o pit-officer ./cmd/pit-officer

# Build all Go packages (no frontend; uses the committed web/dist placeholder).
build-go:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go build {{ go_packages }}

# Run go vet across all packages.
vet:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go vet -all {{ go_packages }}

# Lint Go sources.
lint-go:
    gofmt -l webdist.go cmd internal | (! grep .)
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go vet -all {{ go_packages }}
    GOCACHE={{ go_cache }} GOLANGCI_LINT_CACHE={{ golangci_lint_cache }} golangci-lint run --timeout=5m {{ go_packages }}

# Run all Go tests.
test-go:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go test -count=1 {{ go_packages }}

# Run all Go tests with the race detector.
test-go-race:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go test -race -count=1 {{ go_packages }}

# Run all tests with the race detector.
test: test-go-race

# Run pit-officer in local stdio MCP mode.
run-mcp: build
    ./pit-officer mcp

# Run pit-officer in serve mode (MCP over HTTP + dashboard).
run-serve: build
    ./pit-officer serve

# Print the running serve URL and open it (no rebuild; serve must be up).
dashboard:
    ./pit-officer dashboard

# Install frontend deps and build the SPA into web/dist/.
frontend-build:
    cd web && npm install && npm run build

# Build the SPA using already-installed frontend dependencies.
frontend-build-ready:
    cd web && npm run build

# Install frontend dependencies only (no build).
frontend-install:
    cd web && npm install

# Install frontend dependencies from the committed lockfile.
frontend-ci-install:
    cd web && npm ci

# Lint the SPA (ESLint, incl. the i18n no-literal-string guardrail).
frontend-lint:
    cd web && npm run lint

# Typecheck the SPA without emitting build artifacts.
frontend-typecheck:
    cd web && npm run typecheck

# Run the SPA unit tests (Vitest).
frontend-test:
    cd web && npm run test

# Build the stable Docker image from the published OpenPit module.
docker-build tag="pit-officer:local":
    docker build \
        -f {{ justfile_directory() }}/Dockerfile \
        -t {{ tag }} \
        {{ justfile_directory() }}

# Build the native OpenPit runtime library from a local Pit checkout.
dylib-dev pit_checkout="../pit":
    #!/usr/bin/env bash
    set -euo pipefail
    pit_dir={{ quote(pit_checkout) }}
    pit_dir="$(cd "$pit_dir" && pwd)"
    cargo build -p openpit-ffi --release --locked \
        --manifest-path "$pit_dir/Cargo.toml"

# Build the pit-officer binary against a local Pit checkout.
build-dev pit_checkout="../pit": frontend-build (dylib-dev pit_checkout) (_go-dev pit_checkout "build" "-o" "pit-officer" "./cmd/pit-officer")

# Build all Go packages against a local Pit checkout.
build-go-dev pit_checkout="../pit": (dylib-dev pit_checkout) (_go-dev pit_checkout "build" "./...")

# Run go vet against a local Pit checkout.
vet-dev pit_checkout="../pit": (dylib-dev pit_checkout) (_go-dev pit_checkout "vet" "./...")

# Run tests against a local Pit checkout.
test-dev pit_checkout="../pit": (dylib-dev pit_checkout) (_go-dev pit_checkout "test" "-race" "-count=1" "./...")

# Run pit-officer in stdio MCP mode against a local Pit checkout.
run-mcp-dev pit_checkout="../pit": (build-dev pit_checkout)
    #!/usr/bin/env bash
    set -euo pipefail
    pit_dir={{ quote(pit_checkout) }}
    pit_dir="$(cd "$pit_dir" && pwd)"
    runtime_lib="$pit_dir/target/release/{{ native_runtime_name }}"
    OPENPIT_RUNTIME_LIBRARY_PATH="$runtime_lib" ./pit-officer mcp

# Run pit-officer in serve mode against a local Pit checkout.
run-serve-dev pit_checkout="../pit": (build-dev pit_checkout)
    #!/usr/bin/env bash
    set -euo pipefail
    pit_dir={{ quote(pit_checkout) }}
    pit_dir="$(cd "$pit_dir" && pwd)"
    runtime_lib="$pit_dir/target/release/{{ native_runtime_name }}"
    OPENPIT_RUNTIME_LIBRARY_PATH="$runtime_lib" ./pit-officer serve

# Run a Go command with a temporary workspace using the local OpenPit binding.
_go-dev pit_checkout +go_args:
    #!/usr/bin/env bash
    set -euo pipefail
    officer_dir={{ quote(justfile_directory()) }}
    pit_dir={{ quote(pit_checkout) }}
    pit_dir="$(cd "$pit_dir" && pwd)"
    runtime_lib="$pit_dir/target/release/{{ native_runtime_name }}"
    work_dir="$(mktemp -d)"
    trap 'rm -rf "$work_dir"' EXIT
    (
        cd "$work_dir"
        go work init "$officer_dir" "$pit_dir/bindings/go"
    )
    CGO_ENABLED=1 \
        GOWORK="$work_dir/go.work" \
        OPENPIT_RUNTIME_LIBRARY_PATH="$runtime_lib" \
        go {{ go_args }}

# Seed a running Officer instance with demo accounts, balances, orders, and trades.
seed:
    python3 {{ justfile_directory() }}/tools/seed/seed.py
