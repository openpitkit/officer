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
# Please see https://officer.openpit.dev and the OWNERS file for details.

# Development shortcuts for the pit-officer module.
#
# Default recipes build against the published OpenPit module pinned in go.mod.
# Recipes ending in -dev build against a local Pit checkout without modifying
# go.mod or go.sum.

set dotenv-load
set dotenv-path := ".github/ci-versions.env"
set windows-shell := ["cmd.exe", "/c"]

python := env_var_or_default("PYTHON", if os_family() == "windows" { "python" } else { "python3" })
just_helper := "scripts/just_helpers.py"
officer_binary := if os_family() == "windows" { "pit-officer.exe" } else { "pit-officer" }
go_toolchain := "go" + env_var("CI_GO")
go_version := env_var("CI_GO")
go_cache := env_var_or_default("GOCACHE", justfile_directory() / ".tmp" / "go-build-cache")
golangci_lint_cache := env_var_or_default("GOLANGCI_LINT_CACHE", justfile_directory() / ".tmp" / "golangci-lint-cache")
go_packages := ". ./cmd/... ./internal/... ./examples/... ./app/... ./openapp/..."
go_dirs := "webdist.go cmd internal examples app openapp"
export GOTOOLCHAIN := go_toolchain
export GOCACHE := go_cache
export GOLANGCI_LINT_CACHE := golangci_lint_cache
export CGO_ENABLED := "1"

# Build the pit-officer binary.
build: build-release

# Check that the C toolchain required by cgo is available.
check-cgo-toolchain:
    {{ python }} {{ just_helper }} check-cgo-toolchain

# Build the pit-officer binary with debug compiler flags.
build-debug: check-cgo-toolchain frontend-install build-js
    {{ python }} {{ just_helper }} go . build "-gcflags=all=-N -l" -o {{ officer_binary }} ./cmd/pit-officer

# Build the optimized pit-officer binary.
build-release: check-cgo-toolchain frontend-install build-js
    {{ python }} {{ just_helper }} go . build -o {{ officer_binary }} ./cmd/pit-officer

# Build all Go packages (no frontend; uses the committed web/dist placeholder).
build-go: build-go-release

# Build all Go packages with debug compiler flags.
build-go-debug:
    {{ python }} {{ just_helper }} go . build "-gcflags=all=-N -l" {{ go_packages }}
    {{ python }} {{ just_helper }} go framework build "-gcflags=all=-N -l" ./...

# Build all Go packages optimized.
build-go-release:
    {{ python }} {{ just_helper }} go . build {{ go_packages }}
    {{ python }} {{ just_helper }} go framework build ./...

# Build the SPA using already-installed frontend dependencies.
build-js:
    cd web && npm run build

# Build the published frontend library package.
build-js-lib:
    cd web && npm run build:lib

# Check formatting, lint, build, and test the result.
check: check-release

# Check formatting, lint, build, and test the debug result.
check-debug: check-dry-debug build-js build-js-lib

# Check formatting, lint, build, and test the optimized result.
check-release: check-dry-release build-js build-js-lib

# Check formatting, lint, build, and test against a local Pit checkout.
check-dev pit_checkout="../pit": (check-release-dev pit_checkout)

# Check formatting, lint, build, and test a debug local Pit checkout.
check-debug-dev pit_checkout="../pit": (check-dry-debug-dev pit_checkout) build-js build-js-lib

# Check formatting, lint, build, and test an optimized local Pit checkout.
check-release-dev pit_checkout="../pit": (check-dry-release-dev pit_checkout) build-js build-js-lib

# Lint and test the result (non-mutating).
check-dry: check-dry-release

# Lint and test the debug result (non-mutating).
[parallel]
check-dry-debug: lint-all test-all-debug

# Lint and test the optimized result (non-mutating).
[parallel]
check-dry-release: lint-all test-all-release

# Lint and test against a local Pit checkout (non-mutating).
check-dry-dev pit_checkout="../pit": (check-dry-release-dev pit_checkout)

# Lint and test a debug local Pit checkout (non-mutating).
check-dry-debug-dev pit_checkout="../pit": (lint-all-debug-dev pit_checkout) (test-all-debug-dev pit_checkout)

# Lint and test an optimized local Pit checkout (non-mutating).
check-dry-release-dev pit_checkout="../pit": (lint-all-release-dev pit_checkout) (test-all-release-dev pit_checkout)

# Check formatting, lint, build, and test Go.
check-go: check-go-release

# Check formatting, lint, build, and test Go with debug compiler flags.
check-go-debug: check-go-dry-debug

# Check formatting, lint, build, and test optimized Go.
check-go-release: check-go-dry-release

# Check formatting, lint, build, and test Go against a local Pit checkout.
check-go-dev pit_checkout="../pit": (check-go-release-dev pit_checkout)

# Check formatting, lint, build, and test debug Go against a local Pit checkout.
check-go-debug-dev pit_checkout="../pit": (check-go-dry-debug-dev pit_checkout)

# Check formatting, lint, build, and test optimized Go against a local Pit checkout.
check-go-release-dev pit_checkout="../pit": (check-go-dry-release-dev pit_checkout)

# Lint, build, and test Go (non-mutating).
check-go-dry: check-go-dry-release

# Lint, build, and test Go with debug compiler flags (non-mutating).
[parallel]
check-go-dry-debug: check-cgo-toolchain lint-go build-go-debug test-go-debug test-go-race

# Lint, build, and test optimized Go (non-mutating).
[parallel]
check-go-dry-release: check-cgo-toolchain lint-go build-go-release test-go-release test-go-race

# Lint, build, and test Go against a local Pit checkout (non-mutating).
check-go-dry-dev pit_checkout="../pit": (check-go-dry-release-dev pit_checkout)

# Lint, build, and test debug Go against a local Pit checkout (non-mutating).
check-go-dry-debug-dev pit_checkout="../pit": (lint-go-debug-dev pit_checkout) (build-go-debug-dev pit_checkout) (test-go-debug-dev pit_checkout) (test-go-race-dev pit_checkout)

# Lint, build, and test optimized Go against a local Pit checkout (non-mutating).
check-go-dry-release-dev pit_checkout="../pit": (lint-go-release-dev pit_checkout) (build-go-release-dev pit_checkout) (test-go-release-dev pit_checkout) (test-go-race-dev pit_checkout)

# Lint, build, and test JS/TypeScript.
check-js: check-js-dry build-js build-js-lib

# Lint and test JS/TypeScript (non-mutating).
[parallel]
check-js-dry: lint-js test-js

# Run go vet across all packages.
vet:
    {{ python }} {{ just_helper }} go . vet -all {{ go_packages }}

# Update Go module metadata.
tidy:
    {{ python }} {{ just_helper }} go . mod tidy -go={{ go_version }}
    {{ python }} {{ just_helper }} go framework mod tidy -go={{ go_version }}

# Lint all.
[parallel]
lint-all: lint-go lint-js

# Lint all against a local Pit checkout.
lint-all-dev pit_checkout="../pit": (lint-all-release-dev pit_checkout)

# Lint all against a debug local Pit checkout.
lint-all-debug-dev pit_checkout="../pit": (lint-go-debug-dev pit_checkout) lint-js

# Lint all against an optimized local Pit checkout.
lint-all-release-dev pit_checkout="../pit": (lint-go-release-dev pit_checkout) lint-js

# Lint Go sources.
lint-go:
    {{ python }} {{ just_helper }} check-gofmt {{ go_dirs }}
    {{ python }} {{ just_helper }} check-gofmt framework
    {{ python }} {{ just_helper }} go . mod tidy -go={{ go_version }} -diff
    {{ python }} {{ just_helper }} go framework mod tidy -go={{ go_version }} -diff
    {{ python }} {{ just_helper }} go . vet -all {{ go_packages }}
    {{ python }} {{ just_helper }} go framework vet -all ./...
    {{ python }} {{ just_helper }} go-tool . golangci-lint run --timeout=5m {{ go_packages }}
    {{ python }} {{ just_helper }} go-tool framework golangci-lint run --timeout=5m ./...

# Lint Go sources against a local Pit checkout.
lint-go-dev pit_checkout="../pit": (lint-go-release-dev pit_checkout)

# Lint Go sources against a debug local Pit checkout.
lint-go-debug-dev pit_checkout="../pit": (dylib-debug-dev pit_checkout)
    {{ python }} {{ just_helper }} check-gofmt {{ go_dirs }}
    {{ python }} {{ just_helper }} check-gofmt framework
    go mod tidy -go={{ go_version }} -diff
    cd framework && go mod tidy -go={{ go_version }} -diff
    just _go-dev-mode debug {{ quote(pit_checkout) }} "." "vet" "-all" {{ go_packages }}
    just _go-dev-mode debug {{ quote(pit_checkout) }} "framework" "vet" "-all" "./..."
    just _go-tool-dev-mode debug {{ quote(pit_checkout) }} "." "golangci-lint" "run" "--timeout=5m" {{ go_packages }}
    just _go-tool-dev-mode debug {{ quote(pit_checkout) }} "framework" "golangci-lint" "run" "--timeout=5m" "./..."

# Lint Go sources against an optimized local Pit checkout.
lint-go-release-dev pit_checkout="../pit": (dylib-release-dev pit_checkout)
    {{ python }} {{ just_helper }} check-gofmt {{ go_dirs }}
    {{ python }} {{ just_helper }} check-gofmt framework
    go mod tidy -go={{ go_version }} -diff
    cd framework && go mod tidy -go={{ go_version }} -diff
    just _go-dev-mode release {{ quote(pit_checkout) }} "." "vet" "-all" {{ go_packages }}
    just _go-dev-mode release {{ quote(pit_checkout) }} "framework" "vet" "-all" "./..."
    just _go-tool-dev-mode release {{ quote(pit_checkout) }} "." "golangci-lint" "run" "--timeout=5m" {{ go_packages }}
    just _go-tool-dev-mode release {{ quote(pit_checkout) }} "framework" "golangci-lint" "run" "--timeout=5m" "./..."

# Check formatting, lint, and typecheck JS/TypeScript sources.
lint-js:
    cd web && npm run format:check
    cd web && npm run lint
    cd web && npm run typecheck

# Run all tests.
[parallel]
test-all: test-all-release

# Run all tests with debug compiler flags.
[parallel]
test-all-debug: test-go-debug test-go-race test-js

# Run all optimized tests.
[parallel]
test-all-release: test-go-release test-go-race test-js

# Run all tests against a local Pit checkout.
test-all-dev pit_checkout="../pit": (test-all-release-dev pit_checkout)

# Run all tests against a debug local Pit checkout.
test-all-debug-dev pit_checkout="../pit": (test-go-debug-dev pit_checkout) (test-go-race-dev pit_checkout) test-js

# Run all tests against an optimized local Pit checkout.
test-all-release-dev pit_checkout="../pit": (test-go-release-dev pit_checkout) (test-go-race-dev pit_checkout) test-js

# Run all Go tests.
test-go: test-go-release

# Run all Go tests with debug compiler flags.
test-go-debug:
    {{ python }} {{ just_helper }} go . test "-gcflags=all=-N -l" -count=1 {{ go_packages }}
    {{ python }} {{ just_helper }} go framework test "-gcflags=all=-N -l" -count=1 ./...

# Run all optimized Go tests.
test-go-release:
    {{ python }} {{ just_helper }} go . test -count=1 {{ go_packages }}
    {{ python }} {{ just_helper }} go framework test -count=1 ./...

# Run all Go tests against a local Pit checkout.
test-go-dev pit_checkout="../pit": (test-go-release-dev pit_checkout)

# Run all Go tests against a debug local Pit checkout.
test-go-debug-dev pit_checkout="../pit": (dylib-debug-dev pit_checkout)
    just _go-dev-mode debug {{ quote(pit_checkout) }} "." "test" "-gcflags=all=-N -l" "-count=1" {{ go_packages }}
    just _go-dev-mode debug {{ quote(pit_checkout) }} "framework" "test" "-gcflags=all=-N -l" "-count=1" "./..."

# Run all Go tests against an optimized local Pit checkout.
test-go-release-dev pit_checkout="../pit": (dylib-release-dev pit_checkout)
    just _go-dev-mode release {{ quote(pit_checkout) }} "." "test" "-count=1" {{ go_packages }}
    just _go-dev-mode release {{ quote(pit_checkout) }} "framework" "test" "-count=1" "./..."

# Run all Go tests with the race detector.
[unix]
test-go-race:
    {{ python }} {{ just_helper }} go . test -race -count=1 {{ go_packages }}
    {{ python }} {{ just_helper }} go framework test -race -count=1 ./...
[windows]
test-go-race:
    @echo Skipping Go race tests on Windows: Go ThreadSanitizer is not compatible with the CGo toolchain.

# Run all Go tests with the race detector against a local Pit checkout.
[unix]
test-go-race-dev pit_checkout="../pit": (dylib-release-dev pit_checkout)
    just _go-dev-mode release {{ quote(pit_checkout) }} "." "test" "-race" "-count=1" {{ go_packages }}
    just _go-dev-mode release {{ quote(pit_checkout) }} "framework" "test" "-race" "-count=1" "./..."
[windows]
test-go-race-dev pit_checkout="../pit":
    @echo Skipping Go race tests on Windows: Go ThreadSanitizer is not compatible with the CGo toolchain.

# Run JS/TypeScript tests.
test-js:
    cd web && npm run test

# Run all tests.
test: test-all

# Format all.
[parallel]
fmt-all: fmt-go fmt-js

# Format Go.
fmt-go:
    gofmt -w {{ go_dirs }}
    cd framework && gofmt -w .

# Format JS/TypeScript sources.
fmt-js:
    cd web && npm run format

# Run pit-officer in local stdio MCP mode.
run-mcp: build-release
    {{ python }} {{ just_helper }} run-officer mcp

# Run pit-officer in local stdio MCP mode with debug compiler flags.
run-mcp-debug: build-debug
    {{ python }} {{ just_helper }} run-officer mcp

# Run pit-officer in serve mode (MCP over HTTP + dashboard).
run-serve: build-release
    {{ python }} {{ just_helper }} run-officer serve

# Run pit-officer in serve mode with debug compiler flags.
run-serve-debug: build-debug
    {{ python }} {{ just_helper }} run-officer serve

# Print the running serve URL and open it (no rebuild; serve must be up).
dashboard:
    {{ python }} {{ just_helper }} run-officer dashboard

# Install frontend dependencies only (no build).
frontend-install:
    cd web && npm install

# Install frontend dependencies from the committed lockfile.
frontend-ci-install:
    cd web && npm ci

# Build the stable Docker image from the published OpenPit module.
docker-build tag="pit-officer:local":
    docker build --build-arg GO_VERSION={{ go_version }} -f {{ justfile_directory() }}/Dockerfile -t {{ tag }} {{ justfile_directory() }}

# Build the native OpenPit runtime library from a local Pit checkout.
dylib-dev pit_checkout="../pit": (dylib-release-dev pit_checkout)

# Build the native OpenPit runtime library from a local Pit checkout in debug mode.
dylib-debug-dev pit_checkout="../pit":
    {{ python }} {{ just_helper }} dylib-dev debug {{ quote(pit_checkout) }}

# Build the native OpenPit runtime library from a local Pit checkout optimized.
dylib-release-dev pit_checkout="../pit":
    {{ python }} {{ just_helper }} dylib-dev release {{ quote(pit_checkout) }}

# Build the pit-officer binary against a local Pit checkout.
build-dev pit_checkout="../pit": (build-release-dev pit_checkout)

# Build the pit-officer binary against a debug local Pit checkout.
build-debug-dev pit_checkout="../pit": frontend-install build-js (dylib-debug-dev pit_checkout) (_go-dev-mode "debug" pit_checkout "." "build" "-gcflags=all=-N -l" "-o" officer_binary "./cmd/pit-officer")

# Build the pit-officer binary against an optimized local Pit checkout.
build-release-dev pit_checkout="../pit": frontend-install build-js (dylib-release-dev pit_checkout) (_go-dev-mode "release" pit_checkout "." "build" "-o" officer_binary "./cmd/pit-officer")

# Build all Go packages against a local Pit checkout.
build-go-dev pit_checkout="../pit": (build-go-release-dev pit_checkout)

# Build all Go packages against a debug local Pit checkout.
build-go-debug-dev pit_checkout="../pit": (dylib-debug-dev pit_checkout)
    just _go-dev-mode debug {{ quote(pit_checkout) }} "." "build" "-gcflags=all=-N -l" {{ go_packages }}
    just _go-dev-mode debug {{ quote(pit_checkout) }} "framework" "build" "-gcflags=all=-N -l" "./..."

# Build all Go packages against an optimized local Pit checkout.
build-go-release-dev pit_checkout="../pit": (dylib-release-dev pit_checkout)
    just _go-dev-mode release {{ quote(pit_checkout) }} "." "build" {{ go_packages }}
    just _go-dev-mode release {{ quote(pit_checkout) }} "framework" "build" "./..."

# Run go vet against a local Pit checkout.
vet-dev pit_checkout="../pit": (vet-release-dev pit_checkout)

# Run go vet against a debug local Pit checkout.
vet-debug-dev pit_checkout="../pit": (dylib-debug-dev pit_checkout)
    just _go-dev-mode debug {{ quote(pit_checkout) }} "." "vet" {{ go_packages }}
    just _go-dev-mode debug {{ quote(pit_checkout) }} "framework" "vet" "./..."

# Run go vet against an optimized local Pit checkout.
vet-release-dev pit_checkout="../pit": (dylib-release-dev pit_checkout)
    just _go-dev-mode release {{ quote(pit_checkout) }} "." "vet" {{ go_packages }}
    just _go-dev-mode release {{ quote(pit_checkout) }} "framework" "vet" "./..."

# Run tests against a local Pit checkout.
test-dev pit_checkout="../pit": (test-all-release-dev pit_checkout)

# Run debug tests against a local Pit checkout.
test-debug-dev pit_checkout="../pit": (test-all-debug-dev pit_checkout)

# Run optimized tests against a local Pit checkout.
test-release-dev pit_checkout="../pit": (test-all-release-dev pit_checkout)

# Run pit-officer in stdio MCP mode against a local Pit checkout.
run-mcp-dev pit_checkout="../pit": (build-release-dev pit_checkout)
    {{ python }} {{ just_helper }} run-officer-dev release {{ quote(pit_checkout) }} mcp

# Run pit-officer in stdio MCP mode against a debug local Pit checkout.
run-mcp-debug-dev pit_checkout="../pit": (build-debug-dev pit_checkout)
    {{ python }} {{ just_helper }} run-officer-dev debug {{ quote(pit_checkout) }} mcp

# Run pit-officer in serve mode against a local Pit checkout.
run-serve-dev pit_checkout="../pit": (build-release-dev pit_checkout)
    {{ python }} {{ just_helper }} run-officer-dev release {{ quote(pit_checkout) }} serve

# Run pit-officer in serve mode against a debug local Pit checkout.
run-serve-debug-dev pit_checkout="../pit": (build-debug-dev pit_checkout)
    {{ python }} {{ just_helper }} run-officer-dev debug {{ quote(pit_checkout) }} serve

# Run a Go command with a temporary workspace using the local OpenPit binding.
_go-dev pit_checkout module_dir +go_args:
    just _go-dev-mode release {{ quote(pit_checkout) }} {{ quote(module_dir) }} {{ go_args }}

# Run a Go command with a temporary workspace using the local OpenPit binding.
_go-dev-mode mode pit_checkout module_dir +go_args:
    {{ python }} {{ just_helper }} go-dev {{ mode }} {{ quote(pit_checkout) }} {{ quote(module_dir) }} {{ go_args }}

# Run a Go-adjacent tool with a temporary workspace using the local OpenPit binding.
_go-tool-dev pit_checkout module_dir +tool_args:
    just _go-tool-dev-mode release {{ quote(pit_checkout) }} {{ quote(module_dir) }} {{ tool_args }}

# Run a Go-adjacent tool with a temporary workspace using the local OpenPit binding.
_go-tool-dev-mode mode pit_checkout module_dir +tool_args:
    {{ python }} {{ just_helper }} go-tool-dev {{ mode }} {{ quote(pit_checkout) }} {{ quote(module_dir) }} {{ tool_args }}

# Seed a running Officer instance with demo accounts, balances, orders, and trades.
seed:
    {{ python }} {{ justfile_directory() }}/tools/seed/seed.py
