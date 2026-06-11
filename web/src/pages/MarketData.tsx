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

import { Page } from "@/components/Page";
import { Button } from "@/components/ui/button";

/** Market Data page — connector management (not yet available). */
export function MarketData() {
  return (
    <Page title="Market Data">
      <div className="flex flex-col gap-4">
        <p className="text-sm text-muted">
          Connector management is not yet available.
        </p>
        <div className="flex gap-2">
          <Button variant="outline" disabled>
            Select connector
          </Button>
          <Button variant="outline" disabled>
            Load market-data connector
          </Button>
        </div>
      </div>
    </Page>
  );
}
