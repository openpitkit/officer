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

import { BookOpen } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { SecurityRow } from "@/components/security/SecurityRow";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";

/**
 * The guide section of Service › Security: the first-run guide, opened again
 * on every step with the settings in force.
 */
export function GuideSection() {
  const { t } = useTranslation("service");
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("security.guide.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        <SecurityRow
          label={t("security.guide.label")}
          actions={
            <Button asChild variant="outline" size="sm">
              <Link to="/setup">
                <BookOpen aria-hidden="true" />
                {t("security.guide.reopen")}
              </Link>
            </Button>
          }
        >
          <p>{t("security.guide.value")}</p>
        </SecurityRow>
      </CardContent>
    </Card>
  );
}
