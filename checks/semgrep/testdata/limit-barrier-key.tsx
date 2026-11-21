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


// Review synthetic shape cases.
function balancePairs(a: Balance, b: Balance) {
  // ok: limit-target-axis-compare
  return a.account === b.account && a.asset === b.asset;
}

function requestPairs(a: AccountModeRequest, b: AccountModeState) {
  // ok: limit-target-template-key
  const key = `${a.mode}|${a.account}`;
  // ok: limit-target-axis-compare
  const same = a.mode === b.mode && a.account === b.account;
  return { key, same };
}

function panelOrientation(a: Limit, b: Limit) {
  // ruleid: limit-target-axis-compare
  return a.scope === b.scope && b.account === a.account;
}

function leadingPanelGuard(row: Limit, l: Limit, x: string, ok: boolean, scope: string, account: string) {
  // ruleid: limit-target-axis-compare
  const first = row.kind === x && row.policy === l.policy && row.account === l.account;
  // ruleid: limit-target-axis-compare
  const second = ok && row.scope === l.scope && row.account === l.account;
  // ruleid: limit-target-axis-compare
  const scalar = row.scope === scope && row.account === account;
  // ok: limit-target-axis-compare
  const single = row.scope === l.scope;
  return { first, second, scalar, single };
}

function positionKeys(limits: Limit[]) {
  return limits.map(limit =>
    // ruleid: limit-target-template-key
    `${limit.account}|${limit.asset}`
  );
}

function targetJSON(l: Limit) {
  // ruleid: limit-target-template-key
  return JSON.stringify([l.policy, l.mode, l.scope, l.account, l.accountGroup, l.asset]);
}

function genericTargetJSON(l) {
  // ruleid: limit-target-template-key
  return JSON.stringify([l.policy, l.scope]);
}

function copiedTarget(l: Limit) {
  // ruleid: limit-target-object
  const target: LimitTarget = { scope: l.scope, account: l.account };
  return target;
}

function copiedDeletion(target: Limit) {
  // ruleid: limit-target-object
  return deleteLimit({ policy: target.policy, scope: target.scope, account: target.account });
}



// Final review synthetic cases.
function matches(limit: Limit, policy: string, scope: string) {
  // ruleid: limit-target-axis-compare
  return limit.policy === policy && limit.scope === scope;
}
const matchesArrow = (limit: Limit, policy: string, scope: string) =>
  // ruleid: limit-target-axis-compare
  limit.policy === policy && limit.scope === scope;
class ParameterMatcher {
  matches(limit: Limit, policy: string, scope: string) {
    // ruleid: limit-target-axis-compare
    return limit.policy === policy && limit.scope === scope;
  }
}
function policyDispatch(target: Limit, form: FormState, limit: Limit) {
  // ok: limit-target-axis-compare
  const endpoint = target.policy === "order_size_limit" && target.mode === null ? "/order" : "/other";
  // ok: limit-target-axis-compare
  const guard = <>{form.policy === "rate_limit" && form.scope === "account" && <span />}</>;
  // ok: limit-target-axis-compare
  const validate = limit.policy === "rate_limit" && limit.scope === "account";
  // ok: limit-target-axis-compare
  const missing = limit.policy === undefined && limit.scope === null;
  return { endpoint, guard, validate, missing };
}
function panelProse(limit: Limit, mark: ReviewMark) {
  // Template literal parts: no working filter found; known false positive.
  // todook: limit-target-template-key
  const text = `unknown policy ${limit.policy} for scope ${limit.scope}`;
  // ok: limit-target-template-key
  const label = [mark.policy, mark.scope].join(" · ");
  // + chain literal parts: no working filter found; known false positive.
  // todook: limit-target-template-key
  const plus = limit.policy + " for scope " + limit.scope;
  return { text, label, plus };
}
function indexedCallbacks(limits: Limit[]) {
  return limits.map((limit, index) =>
    // ruleid: limit-target-template-key
    `${limit.account}|${limit.asset}`
  );
}
function forOfTargets(limits: LimitTarget[]) {
  for (const limit of limits) {
    // ruleid: limit-target-template-key
    const key = `${limit.account}|${limit.asset}`;
    consume(key);
  }
}
function memberDeletion(api: OfficerAPI, limit: Limit) {
  // ruleid: limit-target-object
  return api.deleteLimit({ policy: limit.policy, scope: limit.scope });
}
function letTarget(limit: Limit) {
  // ruleid: limit-target-object
  let target: LimitTarget = { policy: limit.policy, scope: limit.scope };
  return target;
}
function returnedTarget(limit: Limit): LimitTarget {
  // ruleid: limit-target-object
  return { policy: limit.policy, scope: limit.scope };
}

// Final separator expression cases.
function expressionSeparators(limit: Limit, SEP: { key: string }, sep: () => string, a: string, b: string, s: string) {
  // ruleid: limit-target-template-key
  const member = [limit.policy, limit.scope].join(SEP.key);
  // ruleid: limit-target-template-key
  const call = [limit.policy, limit.scope].join(sep());
  // ruleid: limit-target-template-key
  const concat = [limit.policy, limit.scope].join(a + b);
  // ruleid: limit-target-template-key
  const template = [limit.policy, limit.scope].join(`prefix ${s}`);
  return { member, call, concat, template };
}

// Close-iteration synthetic join cases.
function closePanelJoins(limit: Limit, SEP: string) {
  // ruleid: limit-target-template-key
  const implicit = [limit.policy, limit.scope].join();
  // ruleid: limit-target-template-key
  const unknown = [limit.account, limit.asset].join(SEP);
  const proseSeparator = " · ";
  // ruleid: limit-target-template-key
  const identifier = [limit.policy, limit.scope].join(proseSeparator);
  // ok: limit-target-template-key
  const prose = [limit.policy, limit.scope].join(" · ");
  return { implicit, unknown, identifier, prose };
}

// Source: web/src/pages/Limits.tsx:289-296 at 9d7fe3b1.
async function historicalDeleteTarget(target: Limit) {
  // ruleid: limit-target-object
      await deleteLimit({
        mode: target.mode,
        policy: target.policy,
        scope: target.scope,
        account: target.account,
        accountGroup: target.accountGroup,
        asset: target.asset,
      });
}

// Negative source: web/src/components/AccountModeChoice.tsx:104-109; current tree.
// ok: limit-target-axis-compare, limit-target-template-key, limit-target-object
function isStored(request: AccountModeRequest, state: AccountModeState) {
  return (
    request.mode === state.mode &&
    (request.mode === "multi" || request.account === state.account)
  );
}

// Negative source: web/src/framework/api/officerApi.ts:1277-1353; current tree.
// ok: limit-target-axis-compare, limit-target-template-key, limit-target-object
function limitEndpointBody(
  client: ApiClient,
  limit: Limit,
  missingAccount: MissingAccountPolicy,
): { path: string; body: unknown } {
  // Only an account-carrying scope names an account the backend could need to
  // create or reject; a broker/asset/group scope must not send the parameter
  // at all, since the backend ignores it there.
  const query =
    limit.account.trim() !== ""
      ? `?${missingAccountQuery(missingAccount)}`
      : "";
  if (isSpotFundsPnlPolicy(limit.policy)) {
    return {
      path: `${client.baseUrl}/limits/${SPOT_FUNDS_PNL_POLICIES[limit.policy].path}${query}`,
      body: {
        scope: limit.scope,
        account: limit.account,
        accountGroup: limit.accountGroup,
        currency: limit.values.currency,
        lowerBound: limit.values.lower_bound ?? "",
        upperBound: limit.values.upper_bound ?? "",
      },
    };
  }
  switch (limit.policy) {
    case "spot_funds_limit_mode":
      // A global Officer policy: no account axis, so never a missingAccount
      // query, whatever the caller passed.
      return {
        path: `${client.baseUrl}/limits/spot-funds-limit-mode`,
        body: { scope: limit.scope, mode: limitSpotFundsMode(limit) },
      };
    case "rate_limit":
      return {
        path: `${client.baseUrl}/limits/rate${query}`,
        body: {
          scope: limit.scope,
          account: limit.account,
          asset: limit.asset,
          windowMs: limitWindowMs(limit),
          maxOrders: limitMaxOrders(limit),
        },
      };
    case "order_size_limit":
      if (!isOrderSizeLimitMode(limit.mode)) {
        throw new ApiError(
          "Order-size limit mode is missing or unknown",
          "validation",
          400,
        );
      }
      return {
        path: `${client.baseUrl}/limits/order-size${query}`,
        body: {
          scope: limit.scope,
          account: limit.account,
          asset: limit.asset,
          maxQuantity: limit.values.max_quantity ?? "",
          maxNotional: limit.values.max_notional ?? "",
          mode: limit.mode,
        },
      };
    case "position_limit":
      return {
        path: `${client.baseUrl}/limits/position${query}`,
        body: {
          scope: limit.scope,
          account: limit.account,
          asset: limit.asset,
          maxPosition: limit.values.max_position,
        },
      };
    default:
      throw new ApiError(`unknown policy ${limit.policy}`, "validation", 400);
  }
}

// Source: web/src/pages/Limits.tsx:471 at 9d7fe3b1.
// The JSX key expression is unchanged; the surrounding row is a parse stub.
function policyRow(limit: Limit) {
  return <TableRow key={
    // ruleid: limit-target-template-key
    `${limit.policy}|${limit.scope}|${limit.account}|${limit.accountGroup ?? ""}|${limit.asset}|${limit.mode}`
  } />;
}

// Source: web/src/components/welcome/WelcomeQuickStart.tsx:496 at 9d7fe3b1.
  const requireNoDemoBarriers = async (account: string, limits: Limit[]) => {
    const page = await fetchPoliciesPage({ account, limit: MAX_LIST_LIMIT });
    if (page.total > page.items.length) {
      throw new Error(t("welcome.status.barriersUnread", { account }));
    }
    const exists = limits.some((limit) =>
      page.items.some(
        (row) =>
          // ruleid: limit-target-axis-compare
          row.policy === limit.policy &&
          row.mode === limit.mode &&
          row.scope === limit.scope &&
          row.account === limit.account &&
          row.asset === limit.asset,
      ),
    );
    if (exists) {
      throw new Error(t("welcome.status.barriersExist", { account }));
    }
  };

function alternateTargets(limit: Limit, other: Limit) {
  // ruleid: limit-target-template-key
  const template = `${limit.policy}|${limit.account}`;
  // ruleid: limit-target-template-key
  const plus = limit.account + ":" + limit.asset;
  // ruleid: limit-target-template-key
  const joined = [limit.policy, limit.scope, limit.account].join("|");
  // ruleid: limit-target-axis-compare
  const differs = limit.policy !== other.policy || limit.mode !== other.mode;
  // ruleid: limit-target-axis-compare
  const mixed = limit.scope === other.scope && limit.asset !== other.asset;
  // ok: limit-target-template-key, limit-target-axis-compare
  const single = limit.scope === "account_group";
  // ok: limit-target-template-key, limit-target-axis-compare
  const separate = limit.account === other.account && third.asset === fourth.asset;
  return { plus, joined, differs, mixed, single, separate };
}

// Negative source: web/src/pages/LimitDialog.tsx:262.
// ok: limit-target-template-key, limit-target-axis-compare
function fromLimit(limit: Limit): FormState {
  return {
    mode: limit.mode,
    policy: limit.policy as Policy,
    scope: limit.scope as Scope,
    account: limit.account,
    accountGroup: limit.accountGroup,
    asset: limit.asset,
    values: { ...limit.values },
  };
}

// Current call sites from Limits.tsx and WelcomeQuickStart.tsx.
async function sharedTargets(target: LimitTarget, limit: Limit, page: PolicyPage) {
  // ok: limit-target-template-key, limit-target-axis-compare
  await deleteLimit(limitTarget(target));
  // ok: limit-target-template-key, limit-target-axis-compare
  const row = <TableRow key={limitTargetKey(limit)} />;
  const exists = page.items.some(
    // ok: limit-target-template-key, limit-target-axis-compare
    (row) => sameLimitTarget(row, limit),
  );
  return { row, exists };
}

// Shape negatives from the first scan: rendering several axes in a JSX tree
// does not join them into a key. An unrelated + must not change that.
function axisDisplay(limit: Limit, state: string) {
  // ok: limit-target-template-key, limit-target-axis-compare
  return <section>
    <p>{t("state." + state)}</p>
    <p>{limit.scope}</p>
    <p>{limit.account}</p>
    <p>{limit.asset}</p>
  </section>;
}

// Negative source: Positions.tsx:2317,2319,2322,2324; holdings identity.
function positionRow(b: Balance, openKey: string, current: string) {
  // ok: limit-target-template-key, limit-target-axis-compare
  const key = `${b.account}|${b.asset}`;
  // ok: limit-target-template-key, limit-target-axis-compare
  const expanded = openKey === `${b.account}|${b.asset}`;
  // ok: limit-target-template-key, limit-target-axis-compare
  const next = current === `${b.account}|${b.asset}`
    ? null
    // ok: limit-target-template-key, limit-target-axis-compare
    : `${b.account}|${b.asset}`;
  return { key, expanded, next };
}

// Negative source: OrderVerificationPanel.tsx:349-360; a review label.
function reviewLabel(mark: ReviewMark, index: number) {
  // ok: limit-target-template-key, limit-target-axis-compare
  const key = `${mark.policy}:${mark.code}:${index}`;
  // ok: limit-target-template-key, limit-target-axis-compare
  const label = [
    mark.policy,
    present(mark.mode, prefixed("domain:ruleMode")),
    present(mark.code, rejectLabel),
    present(mark.scope, rejectScopeLabel),
    mark.reason,
    mark.details,
  ].filter(Boolean).join(" · ");
  return { key, label };
}

// Negative shape: test execution policy modes are display inputs.
function executionRule(rule: TestRule) {
  // ok: limit-target-template-key, limit-target-axis-compare
  return <section>
    <p>{t(`testPolicy.modes.${rule.mode}`)}</p>
    <input value={rule.mode} />
    {rule.mode === "rest" && <span>{rule.waitKind}</span>}
  </section>;
}
