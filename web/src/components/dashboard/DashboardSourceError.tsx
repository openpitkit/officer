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

import { Badge } from "@/components/ui/badge";

const DETAIL_LIMIT = 40;

function clip(text: string): string {
  return text.length > DETAIL_LIMIT ? `${text.slice(0, DETAIL_LIMIT)}…` : text;
}

export interface DashboardSourceErrorProps {
  /** Leading text: the error or state word, plus the source name when the
   *  chip stands alone rather than inside a `DashboardSourceRow`. */
  title: string;
  /** The source's own error text; shown after a colon. */
  error?: string;
  /** Title of the source's latest diagnostic; shown after a dash when there
   *  is no error text. */
  diagnostic?: string;
}

/** Danger chip for a failed dashboard source, its cause clipped to fit. */
export function DashboardSourceError({
  title,
  error,
  diagnostic,
}: DashboardSourceErrorProps) {
  const detail = error
    ? `: ${clip(error)}`
    : diagnostic
      ? ` - ${clip(diagnostic)}`
      : "";
  return (
    <Badge variant="danger">
      {title}
      {detail}
    </Badge>
  );
}
