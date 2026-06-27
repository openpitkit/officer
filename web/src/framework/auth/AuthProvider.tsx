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

import { useMemo, type ReactNode } from "react";

import {
  AuthContext,
  type AuthContextValue,
  type Permission,
  type User,
} from "./auth-context";
import { allowAllAuth } from "./defaultAuth";

interface AuthProviderProps {
  children: ReactNode;
  /** A complete value override. When omitted, user/hasPermission apply. */
  value?: AuthContextValue;
  /** Convenience overrides used only when value is not given. */
  user?: User | null;
  hasPermission?: (permission: Permission) => boolean;
}

export function AuthProvider({
  children,
  value,
  user,
  hasPermission,
}: AuthProviderProps) {
  const contextValue = useMemo<AuthContextValue>(
    () =>
      value ?? {
        user: user ?? allowAllAuth.user,
        hasPermission: hasPermission ?? allowAllAuth.hasPermission,
      },
    [hasPermission, user, value],
  );

  return (
    <AuthContext.Provider value={contextValue}>{children}</AuthContext.Provider>
  );
}
