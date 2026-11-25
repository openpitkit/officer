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

import { AlertTriangle, Bell } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Notice } from "@/components/Notice";
import { Button } from "@/components/ui/button";

interface NotificationPermissionNoticeProps {
  permission: NotificationPermission | "unsupported";
  failure: string | null;
  requestPermission: () => Promise<void>;
}

export function NotificationPermissionNotice({
  permission,
  failure,
  requestPermission,
}: NotificationPermissionNoticeProps) {
  const { t } = useTranslation("alerts");

  let permissionNotice;
  if (permission === "denied") {
    permissionNotice = (
      <Notice
        tone="warn"
        icon={AlertTriangle}
        title={t("browserNotifications.denied")}
      >
        <p>{t("browserNotifications.deniedDetail")}</p>
      </Notice>
    );
  } else if (permission === "default") {
    permissionNotice = (
      <Notice
        tone="info"
        icon={Bell}
        title={t("browserNotifications.default")}
        action={
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => void requestPermission()}
          >
            {t("browserNotifications.allow")}
          </Button>
        }
      >
        <p>{t("browserNotifications.defaultDetail")}</p>
      </Notice>
    );
  } else {
    permissionNotice = (
      <Notice
        tone="info"
        icon={Bell}
        title={t(
          permission === "granted"
            ? "browserNotifications.granted"
            : "browserNotifications.unsupported",
        )}
      />
    );
  }

  return (
    <>
      {failure !== null && (
        <Notice
          tone="danger"
          icon={AlertTriangle}
          title={t("browserNotifications.failed")}
          role="alert"
        >
          <p>{failure}</p>
        </Notice>
      )}
      {permissionNotice}
    </>
  );
}
