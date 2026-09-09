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

// Auth types, context, and hooks are kept apart from the provider component so
// the provider file exports only a component (Fast Refresh).

import { createContext, useContext } from "react";

import { allowAllAuth } from "./defaultAuth";

/** A permission identifier. This package declares none. */
export type Permission = string;

/**
 * The current user. Deliberately thin: the built-in default has no real users.
 */
export interface User {
  id?: string;
  name?: string;
  permissions?: readonly Permission[];
}

export interface AuthContextValue {
  /** The current user, or null when anonymous. */
  user: User | null;
  /** Whether the current user may perform the given permission. */
  hasPermission: (permission: Permission) => boolean;
}

export const AuthContext = createContext<AuthContextValue | null>(null);

/** Access the current auth strategy. */
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  // Existing page tests render registry-consuming components without providers;
  // the application must therefore tolerate a missing provider as allow-all.
  return ctx ?? allowAllAuth;
}

/** Access the current user, or null for the built-in default. */
export function useUser(): User | null {
  return useAuth().user;
}

/** Access the current permission predicate. */
export function useHasPermission(): (permission: Permission) => boolean {
  return useAuth().hasPermission;
}
