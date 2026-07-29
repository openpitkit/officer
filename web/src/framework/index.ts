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

export {
  ApiError,
  createApiClient,
  type ApiClient,
  type ApiClientConfig,
  type ApiErrorCode,
  type RequestOptions,
} from "./api/createApiClient";
export {
  ApiClientProvider,
  useApiClient,
  useOfficerApi,
} from "./api/ApiClientProvider";
export { AuthProvider } from "./auth/AuthProvider";
export { allowAllAuth } from "./auth/defaultAuth";
export {
  useAuth,
  useHasPermission,
  useUser,
  type AuthContextValue,
  type Permission,
  type User,
} from "./auth/auth-context";
export {
  createOfficerApi,
  normalizeSigningKeysStatus,
  serviceLogsDownloadUrl,
  type CancelOrderBody,
  type ConfirmOrderBody,
  type CreateOrderBody,
  type CreateOrderResult,
  type SubmittedOrder,
  type AuditFilter,
  type ExecutionReportBody,
  type GlobalAdjustmentsFilter,
  type MarketDataSymbolSearchInput,
  type OfficerApi,
  type TradesFilter,
} from "./api/officerApi";
export { AppRoutes, RedirectPreservingQuery } from "./AppRoutes";
export {
  AUTOCOMPLETE_SUGGESTION_LIMIT,
  MAX_LIST_LIMIT,
} from "./constants";
export { DashboardWidgets } from "./DashboardWidgets";
export { RowActions as RegistryRowActions } from "./RowActions";
export { Sidebar } from "./Sidebar";
export { SidebarProvider } from "../components/SidebarContext";
export { useSidebar, type SidebarState } from "../components/sidebar-context";
export {
  ActionButton,
  BlockButton,
  CloneButton,
  CopyIdButton,
  DeleteButton,
  EditButton,
  AutocompleteFilterField,
  DateTimeField,
  ExactIdField,
  FieldLabel,
  FilterBar,
  FilterByButton,
  FilterChip,
  FilterOperatorSelect,
  ColumnHeader,
  HistoryButton,
  IdCell,
  MoreFiltersButton,
  NumberRangeFilter,
  OnlineFilterField,
  OPERATORS,
  OrdersButton,
  PoliciesButton,
  PositionsButton,
  reportInvalidFilterControls,
  RowActions,
  Segmented,
  ShareLinkButton,
  TradesButton,
  TradingButton,
  SortableHeader,
  TextFilter,
  TimeRangeFilter,
  useOpenInNewTabHint,
  ViewEntityButton,
  type ActionButtonProps,
  type AutocompleteFilterFieldProps,
  type CopyIdButtonProps,
  type DateTimeFieldProps,
  type ColumnHeaderProps,
  type ExactIdFieldProps,
  type FieldLabelProps,
  type FilterBarProps,
  type FilterChipProps,
  type GlobalFilterToggleProps,
  type FilterOperatorSelectProps,
  type FilterValueType,
  type IdCellProps,
  type MoreFiltersButtonProps,
  type NumberRangeFilterProps,
  type OnlineFilterFieldProps,
  type OperatorOption,
  type RowActionProps,
  type RowActionsProps,
  type SegmentedOption,
  type SegmentedProps,
  type ShareLinkButtonProps,
  type SortableHeaderProps,
  type SortDirection,
  type TextFilterProps,
  type TimeRangeFilterProps,
} from "../components/data";
export {
  getRowActions,
  registerRowAction,
  resetRowActions,
  unregisterRowAction,
  type RowActionEntry,
} from "./registries/actions";
export {
  getNav,
  registerNav,
  resetNav,
  unregisterNav,
  type NavEntry,
  type NavSection,
} from "./registries/nav";
export {
  getPage,
  registerPage,
  resetPages,
  unregisterPage,
  type PageEntry,
} from "./registries/pages";
export { resetRegistries } from "./registries/reset";
export {
  getRoutes,
  registerRoute,
  resetRoutes,
  unregisterRoute,
  type RouteEntry,
} from "./registries/routes";
export {
  getAllowedScopes,
  getPolicies,
  getPolicyCatalogEntry,
  getPolicyKinds,
  getScopes,
  isPolicy,
  isScope,
  kindHint,
  policyCatalogDescription,
  policyCatalogLabel,
  policyFieldHint,
  policyFieldLabel,
  policyLabel,
  registerPolicy,
  registerScope,
  resetVocabulary,
  scopeHasAccount,
  scopeHasAsset,
  scopeLabel,
  unregisterPolicy,
  unregisterScope,
  type KindSpec,
  type Policy,
  type PolicyCatalogEntry,
  type PolicyField,
  type PolicyRegistration,
  type Scope,
} from "./registries/vocabulary";
export {
  getWidgets,
  registerWidget,
  resetWidgets,
  unregisterWidget,
  type WidgetEntry,
} from "./registries/widgets";
export {
  registerLocale,
  registerLocaleResourceMap,
  registerLocaleResources,
  resetLocales,
  unregisterLocale,
} from "../i18n";
export type * from "../api/types";
