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
  type CreateOrderBody,
  type CreateOrderResult,
  type SubmittedOrder,
  type AuditFilter,
  type ExecutionReportBody,
  type GlobalAdjustmentsFilter,
  type MarketDataSymbolSearchInput,
  type MissingAccountPolicy,
  type OfficerApi,
  type TradesFilter,
} from "./api/officerApi";
export { AppRoutes, RedirectPreservingQuery } from "./AppRoutes";
export { AUTOCOMPLETE_SUGGESTION_LIMIT, MAX_LIST_LIMIT } from "./constants";
export { DashboardWidgets } from "./DashboardWidgets";
export { RowActions as RegistryRowActions } from "./RowActions";
export { Sidebar } from "./Sidebar";
export { SidebarProvider } from "../components/SidebarContext";
export { useSidebar, type SidebarState } from "../components/sidebar-context";
export {
  Tabs,
  TabsList,
  TabsTrigger,
  TabsContent,
} from "../components/ui/tabs";
export { Notice, type NoticeProps } from "../components/Notice";
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
  VenueAccessButton,
  ViewEntityButton,
  VenueFilter,
  VENUE_FILTER_ANY,
  VENUE_FILTER_NONE,
  VENUE_FILTER_WIDTH,
  normalizeVenueFilterValue,
  venueConnectionFilterValue,
  venueConnectionLabel,
  venueFilterConnectionId,
  venueFilterValueLabel,
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
  type VenueFilterProps,
} from "../components/data";
export {
  AddProviderButton,
  ProviderGuideDialog,
  ProviderIcon,
  ProviderTile,
  VenueCardHeader,
  SymbolSearchPanel,
  useSymbolSearch,
  useRowVerification,
  type SymbolSearchState,
  type RowVerificationState,
  type RowVerificationOutcome,
  VerifyResult,
  InstrumentDraftRow,
  InstrumentsTable,
  DeleteInstrumentDialog,
  LinkButton,
  WarnHint,
  AccessBlockedHint,
  accessGrantBlock,
  RevokeAccessDialog,
  VenueAccessList,
  VenueAccessDialog,
  UnsealedKeysWarning,
  DiagnosticPanel,
  type DiagnosticPanelProps,
  type UnsealedKeysWarningProps,
  type LinkButtonProps,
  type WarnHintProps,
  type AccessBlockedHintProps,
  type AccessGrantBlock,
  type RevokeAccessDialogProps,
  type VenueAccessListProps,
  type VenueAccessDialogProps,
  type VenueAccessDialogRow,
  type VenueAccessDialogEditableRow,
  type SymbolSearchPanelProps,
  type SymbolVerificationOutcome,
  type FailedVerificationOutcome,
  type VerifyResultProps,
  type InstrumentDraftFields,
  type InstrumentDraftRowProps,
  type InstrumentTableRow,
  type InstrumentsTableColumn,
  type InstrumentsTableProps,
  type DeleteInstrumentDialogProps,
  type AddProviderButtonProps,
  type ProviderGuideDialogProps,
  type ProviderIconProps,
  type ProviderTileProps,
  type VenueCardHeaderProps,
  type VenueProviderOption,
} from "../components/venues";
export {
  DashboardIssueCount,
  DashboardItemChips,
  DashboardNote,
  DashboardSourceError,
  DashboardSourceRow,
  DashboardStatusCard,
  type DashboardIssueCountProps,
  type DashboardItem,
  type DashboardItemChipsProps,
  type DashboardNoteProps,
  type DashboardSourceErrorProps,
  type DashboardSourceRowProps,
  type DashboardStatusCardProps,
} from "../components/dashboard";
export type { LoadState } from "../api/usePolling";
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
  ADJUSTMENT_TYPES,
  adjustmentTypeLabel,
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
  registerPolicy,
  registerScope,
  resetVocabulary,
  scopeHasAccount,
  scopeHasAsset,
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
export { policyFieldLabel, policyLabel, scopeLabel } from "../i18n/codeLabel";
export type * from "../api/types";
