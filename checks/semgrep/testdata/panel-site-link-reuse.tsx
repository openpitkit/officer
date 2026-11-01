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

import { siteLink } from "@/lib/siteLinks";

// ruleid: shared-panel-site-link
const literal = "https://officer.openpit.dev/questions/?officer";
// ruleid: shared-panel-site-link
const template = `https://wiki.openpit.dev/Policies/?officer#${anchor}`;
// ruleid: shared-panel-site-link
const httpLink = 'http://openpit.dev';
// ruleid: shared-panel-site-link
const subdomain = "https://docs.wiki.openpit.dev/";
// ruleid: shared-panel-site-link
const jsx = <a href="https://openpit.dev?officer">OpenPit</a>;
// ok: shared-panel-site-link
const shared = siteLink("officer", "/questions/");
// ok: shared-panel-site-link
// "https://officer.openpit.dev" is a comment, not a panel link.
// ok: shared-panel-site-link
const github = "https://github.com/openpitkit/officer/issues?officer";
// ok: shared-panel-site-link
const youtube = "https://www.youtube.com/watch?v=v34IdYhH5so";
