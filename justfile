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
go_packages := ". ./cmd/... ./internal/... ./examples/... ./app/... ./openapp/..."
go_dirs := "webdist.go cmd internal examples app openapp"

# Build the pit-officer binary.
build: frontend-install build-js
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go build -o pit-officer ./cmd/pit-officer

# Build all Go packages (no frontend; uses the committed web/dist placeholder).
build-go:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go build {{ go_packages }}
    cd framework && GOCACHE={{ go_cache }} CGO_ENABLED=1 go build ./...

# Build the SPA using already-installed frontend dependencies.
build-js:
    cd web && npm run build

# Build the published frontend library package.
build-js-lib:
    cd web && npm run build:lib

# Check formatting, lint, build, and test the result.
check: check-dry build-js build-js-lib

# Lint and test the result (non-mutating).
[parallel]
check-dry: lint-all test-all

# Check formatting, lint, build, and test Go.
check-go: check-go-dry

# Lint, build, and test Go (non-mutating).
[parallel]
check-go-dry: lint-go build-go test-go test-go-race

# Lint, build, and test JS/TypeScript.
check-js: check-js-dry build-js build-js-lib

# Lint and test JS/TypeScript (non-mutating).
[parallel]
check-js-dry: lint-js test-js

# Run go vet across all packages.
vet:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go vet -all {{ go_packages }}

# Lint all.
[parallel]
lint-all: lint-go lint-js

# Lint Go sources.
lint-go:
    gofmt -l {{ go_dirs }} | (! grep .)
    cd framework && gofmt -l . | (! grep .)
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go mod tidy -diff
    cd framework && GOCACHE={{ go_cache }} CGO_ENABLED=1 go mod tidy -diff
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go vet -all {{ go_packages }}
    cd framework && GOCACHE={{ go_cache }} CGO_ENABLED=1 go vet -all ./...
    GOCACHE={{ go_cache }} GOLANGCI_LINT_CACHE={{ golangci_lint_cache }} golangci-lint run --timeout=5m {{ go_packages }}
    cd framework && GOCACHE={{ go_cache }} GOLANGCI_LINT_CACHE={{ golangci_lint_cache }} golangci-lint run --timeout=5m ./...

# Lint and typecheck JS/TypeScript sources.
lint-js:
    cd web && npm run lint
    cd web && npm run typecheck

# Run all tests.
[parallel]
test-all: test-go test-go-race test-js

# Run all Go tests.
test-go:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go test -count=1 {{ go_packages }}
    cd framework && GOCACHE={{ go_cache }} CGO_ENABLED=1 go test -count=1 ./...

# Run all Go tests with the race detector.
test-go-race:
    GOCACHE={{ go_cache }} CGO_ENABLED=1 go test -race -count=1 {{ go_packages }}
    cd framework && GOCACHE={{ go_cache }} CGO_ENABLED=1 go test -race -count=1 ./...

# Run JS/TypeScript tests.
test-js:
    cd web && npm run test

# Run all tests.
test: test-all

# Format all.
[parallel]
fmt-all: fmt-go

# Format Go.
fmt-go:
    gofmt -w {{ go_dirs }}
    cd framework && gofmt -w .

# Run pit-officer in local stdio MCP mode.
run-mcp: build
    ./pit-officer mcp

# Run pit-officer in serve mode (MCP over HTTP + dashboard).
run-serve: build
    ./pit-officer serve

# Print the running serve URL and open it (no rebuild; serve must be up).
dashboard:
    ./pit-officer dashboard

# Install frontend dependencies only (no build).
frontend-install:
    cd web && npm install

# Install frontend dependencies from the committed lockfile.
frontend-ci-install:
    cd web && npm ci

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
build-dev pit_checkout="../pit": frontend-install build-js (dylib-dev pit_checkout) (_go-dev pit_checkout "." "build" "-o" "pit-officer" "./cmd/pit-officer")

# Build all Go packages against a local Pit checkout.
build-go-dev pit_checkout="../pit": (dylib-dev pit_checkout) (_go-dev pit_checkout "." "build" "./...") (_go-dev pit_checkout "framework" "build" "./...")

# Run go vet against a local Pit checkout.
vet-dev pit_checkout="../pit": (dylib-dev pit_checkout) (_go-dev pit_checkout "." "vet" "./...") (_go-dev pit_checkout "framework" "vet" "./...")

# Run tests against a local Pit checkout.
test-dev pit_checkout="../pit": (dylib-dev pit_checkout) (_go-dev pit_checkout "." "test" "-race" "-count=1" "./...") (_go-dev pit_checkout "framework" "test" "-race" "-count=1" "./...")

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
_go-dev pit_checkout module_dir +go_args:
    #!/usr/bin/env bash
    set -euo pipefail
    officer_dir={{ quote(justfile_directory()) }}
    pit_dir={{ quote(pit_checkout) }}
    module_dir={{ quote(module_dir) }}
    pit_dir="$(cd "$pit_dir" && pwd)"
    runtime_lib="$pit_dir/target/release/{{ native_runtime_name }}"
    work_dir="$(mktemp -d)"
    trap 'rm -rf "$work_dir"' EXIT
    (
        cd "$work_dir"
        go work init "$officer_dir" "$officer_dir/framework" "$pit_dir/bindings/go"
    )
    (
        cd "$officer_dir/$module_dir"
        CGO_ENABLED=1 \
            GOWORK="$work_dir/go.work" \
            OPENPIT_RUNTIME_LIBRARY_PATH="$runtime_lib" \
            go {{ go_args }}
    )

# Seed a running Officer instance with demo accounts, balances, orders, and trades.
seed:
    python3 {{ justfile_directory() }}/tools/seed/seed.py
