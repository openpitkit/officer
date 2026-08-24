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

# CI-only aggregate recipes, invoked from .github/workflows/* via:
#   just --justfile pipeline.just <recipe>. Imports the project justfile.

import 'justfile'

# Go lint, build, and test against the published OpenPit module.
ci-go: check-semgrep check-go-dry-release

# JS/TypeScript install, lint, build, and unit tests.
ci-js: frontend-ci-install check-js
