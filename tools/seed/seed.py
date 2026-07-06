# Copyright The Pit Project Owners. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
#
# Please see https://openpit.dev and the OWNERS file for details.
#
# Demo data only. Every account name, group, and note below is fiction: a
# satirical archetype of trader *behaviour*, not a portrait of any real person,
# firm, fund, or event. Any resemblance to a real entity — living, dead, or
# merely insolvent — is coincidental and unintended. The jokes are aimed at
# generic human failings (greed, FOMO, an absent off switch), never at a
# nameable target. Do not seed a production instance with this file.

"""Pit Officer demo seed utility.

Populates a running Pit Officer instance via its public REST API (/api/v1)
with a set of fictional accounts, groups, balances, typed risk limits, orders,
and trades so the team can take demo screenshots and sanity-check behaviour.

Usage:
    python seed.py [--base http://host:port] [--runtime-file PATH]

Base-URL discovery (in order):
  1. --base flag (explicit override).
  2. officer-runtime.json next to this script (or --runtime-file PATH).
  3. officer-runtime.json in the current working directory.
  4. Error — no hard-coded fallback; pass --base or start serve.
"""

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request
from decimal import Decimal
from pathlib import Path
from typing import Any

# ---------------------------------------------------------------------------
# Base-URL discovery
# ---------------------------------------------------------------------------

_RUNTIME_FILE_NAME = "officer-runtime.json"


def _discover_base(explicit: str | None, runtime_file: str | None) -> str:
    if explicit:
        return explicit.rstrip("/")

    candidates: list[Path] = []
    if runtime_file:
        candidates.append(Path(runtime_file))
    # Next to this script.
    candidates.append(Path(__file__).parent / _RUNTIME_FILE_NAME)
    # CWD.
    candidates.append(Path.cwd() / _RUNTIME_FILE_NAME)

    for path in candidates:
        if path.exists():
            try:
                state = json.loads(path.read_text())
                url: str = state.get("url", "").rstrip("/")
                if url:
                    print(f"[discovery] using URL from {path}: {url}")
                    return url
            except Exception as exc:
                print(f"[discovery] could not parse {path}: {exc}", file=sys.stderr)

    print(
        "error: cannot determine the Officer base URL.\n"
        "  Pass --base http://host:port, or start 'pit-officer serve' so that\n"
        "  officer-runtime.json is written next to this script or in the working directory.",
        file=sys.stderr,
    )
    sys.exit(1)


# ---------------------------------------------------------------------------
# HTTP helpers
# ---------------------------------------------------------------------------


def _request(
    method: str,
    url: str,
    body: dict[str, Any] | None = None,
    ok_statuses: tuple[int, ...] = (200, 201),
    conflict_ok: bool = False,
) -> tuple[int, dict[str, Any]]:
    """Make a JSON request and return (status_code, parsed_body)."""
    data: bytes | None = None
    headers: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"

    req = urllib.request.Request(url, data=data, headers=headers, method=method)
    try:
        with urllib.request.urlopen(req) as resp:
            raw = resp.read()
            parsed = json.loads(raw) if raw else {}
            return resp.status, parsed
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        try:
            parsed = json.loads(raw)
        except Exception:
            parsed = {"raw": raw.decode(errors="replace")}
        if conflict_ok and exc.code == 409:
            return 409, parsed
        if exc.code not in ok_statuses:
            raise
        return exc.code, parsed


def _post(base: str, path: str, body: dict[str, Any], conflict_ok: bool = True) -> dict[str, Any]:
    status, data = _request("POST", f"{base}/api/v1{path}", body=body, conflict_ok=conflict_ok)
    return data


def _put(base: str, path: str, body: dict[str, Any]) -> dict[str, Any]:
    _, data = _request("PUT", f"{base}/api/v1{path}", body=body)
    return data


def _get(base: str, path: str) -> dict[str, Any]:
    _, data = _request("GET", f"{base}/api/v1{path}", ok_statuses=(200,))
    return data


# ---------------------------------------------------------------------------
# Seed data
# ---------------------------------------------------------------------------

# An asset is a dictionary record: code is its immutable handle, title its
# display name, and assetClass is an optional classification used by operators.
# An asset class is a dictionary record every asset links to by code. Assets
# reference a class via a foreign key, so the classes must be created first.
# Create body is {code, title, notes}.
ASSET_CLASSES = [
    {"code": "cash", "title": "Cash", "notes": "Fiat and stablecoin balances."},
    {"code": "equity", "title": "Equity", "notes": "Listed company shares."},
    {"code": "index", "title": "Index", "notes": "Market index instruments."},
]

ASSETS = [
    {"code": "USD", "title": "US Dollar", "assetClass": "cash"},
    {"code": "AAPL", "title": "Apple Inc.", "assetClass": "equity"},
    {"code": "MSFT", "title": "Microsoft Corp.", "assetClass": "equity"},
    {"code": "SPX", "title": "S&P 500 Index", "assetClass": "index"},
]

# A group is a dictionary record: code is its immutable handle, title its
# display name. Create body is {code, title, notes}.
GROUPS = [
    {
        "code": "whales",
        "title": "Whales",
        "notes": "Accounts with enough capital to move markets — handle with care.",
    },
    {
        "code": "degens",
        "title": "Degenerates",
        "notes": "High-frequency risk-takers; frequent flyers on the margin-call list.",
    },
    {
        "code": "algos",
        "title": "Algos",
        "notes": "Automated desks. Fast, tireless, and only as sane as their last deploy.",
    },
    {
        "code": "blowups",
        "title": "Blow-ups",
        "notes": "Accounts underwater. Do not lend them more rope.",
    },
]

# Each account: code, title, group, notes, balances (list of adjustment dicts),
# blocked/reason. Code is the immutable handle; title is the display name.
# Balance adjustments use mode "absolute" on the `balance` field (absolute USD
# value). For the underwater desks we also set a large negative `held` to model
# locked losses.
ACCOUNTS = [
    # --- Whales -------------------------------------------------------------
    {
        "code": "whale-bucks",
        "title": "Bucks McMoneyface",
        "group": "whales",
        "notes": "Patriarch of the McMoneyface dynasty. Buys dips; is the dip.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "12500000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "whale-rocket-surgeons",
        "title": "Rocket Surgeons LLC",
        "group": "whales",
        "notes": "Technically we do rocket surgery. Financially, same thing.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "9800000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "whale-gravy-train",
        "title": "Gravy Train Capital",
        "group": "whales",
        "notes": "Wealth so old it predates fiat. Tips the sommelier in basis points.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "21000000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "whale-liquid-courage",
        "title": "Liquid Courage Partners",
        "group": "whales",
        "notes": "Deep pockets, shallow convictions. Buys conviction by the case.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "7400000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "whale-compounding-daily",
        "title": "Compounding Daily LLC",
        "group": "whales",
        "notes": "Started with a dollar and a dream. The dollar did the heavy lifting.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "15300000.00"}},
        ],
        "blocked": False,
    },
    # --- Degenerates --------------------------------------------------------
    {
        "code": "degen-diamond-hands",
        "title": "Diamond Hands Capital",
        "group": "degens",
        "notes": "We never sell. Not once. Not ever. (We always sell at the bottom.)",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "3200000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "degen-fomo",
        "title": "FOMO Ventures",
        "group": "degens",
        "notes": "Late to every trade since inception. Holding the bag professionally.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "750000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "degen-hindsight",
        "title": "Hindsight Asset Mgmt",
        "group": "degens",
        "notes": "Our research is flawless — six months after the fact.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "1100000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "degen-yolo",
        "title": "YOLO Capital Mgmt",
        "group": "degens",
        "notes": "Position sizing is for people who plan to be here next quarter.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "640000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "degen-leverage-regret",
        "title": "Leverage & Regret LLP",
        "group": "degens",
        "notes": "Full-service firm: we supply the leverage, you supply the regret.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "980000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "degen-revenge-trade",
        "title": "Revenge Trade Partners",
        "group": "degens",
        "notes": "The market took something from us. We're getting it back, one fat finger at a time.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "430000.00"}},
        ],
        "blocked": False,
    },
    # --- Algos --------------------------------------------------------------
    {
        "code": "algo-infinite-loop",
        "title": "Infinite Loop Capital",
        "group": "algos",
        "notes": (
            "Fully automated desk. The strategy was flawless; the off switch was "
            "theoretical. Kept blocked as a cautionary exhibit for pre-trade risk."
        ),
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "500000.00"}},
        ],
        "blocked": True,
        "block_reason": "Trading halt — runaway-algorithm kill-switch activated.",
    },
    {
        "code": "algo-null-pointer",
        "title": "Null Pointer Securities",
        "group": "algos",
        "notes": "Quant shop. Occasionally trades on data that doesn't exist. The P&L agrees.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "2600000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "algo-backtest-overfit",
        "title": "Backtest Overfit Labs",
        "group": "algos",
        "notes": "100% win rate in simulation. Reality has filed a formal complaint.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "1750000.00"}},
        ],
        "blocked": False,
    },
    # --- Blow-ups -----------------------------------------------------------
    {
        "code": "blowup-margin-call",
        "title": "Margin Call Partners",
        "group": "blowups",
        "notes": "Partners since 2019. Still partners. The margin desk is not a partner.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "85000.00"}},
            # Held models outstanding reserve from underwater positions.
            {"asset": "USD", "held": {"mode": "absolute", "value": "-240000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "blowup-buy-high-sell-low",
        "title": "Buy High Sell Low Inc",
        "group": "blowups",
        "notes": "Strategy document available on request. Results as advertised.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "42000.00"}},
            {"asset": "USD", "held": {"mode": "absolute", "value": "-110000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "blowup-catching-knives",
        "title": "Catching Knives LLC",
        "group": "blowups",
        "notes": "Specialists in falling assets. The catching is going great; the holding, less so.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "60000.00"}},
            {"asset": "USD", "held": {"mode": "absolute", "value": "-180000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "blowup-negative-carry",
        "title": "Negative Carry Capital",
        "group": "blowups",
        "notes": "Every position costs money to hold. They hold many positions.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "33000.00"}},
            {"asset": "USD", "held": {"mode": "absolute", "value": "-90000.00"}},
        ],
        "blocked": False,
    },
    {
        "code": "blowup-sunk-cost",
        "title": "Sunk Cost Brothers",
        "group": "blowups",
        "notes": (
            "A legendary blow-up, preserved as a museum piece. House strategy was "
            "'average down forever'. There is no resemblance to any real firm; the "
            "fallacy is the joke, not a name. Do not extend credit."
        ),
        "balances": [
            # Nominal balance left from the estate.
            {"asset": "USD", "balance": {"mode": "absolute", "value": "1.00"}},
            # Catastrophic negative held: the underwater desk, deep in the red.
            {"asset": "USD", "held": {"mode": "absolute", "value": "-47300000.00"}},
        ],
        "blocked": True,
        "block_reason": "Regulatory hold — pending liquidation proceedings.",
    },
]

# Typed risk limits. Each entry has a `kind` selecting the endpoint and response
# shape; the rest are the per-kind body fields:
#   rate       -> PUT /limits/rate        {scope, account, asset, windowMs, maxOrders}
#   order_size -> PUT /limits/order-size  {scope, account, asset, maxQuantity, maxNotional}
#   pnl_bounds -> PUT /limits/pnl-bounds  {scope, account, asset, lowerBound, upperBound, initialPnl}
# Scope axes: account required for account/account_asset; asset required for
# asset/account_asset. Allowed scopes differ per kind (the engine validates):
#   rate       broker | asset | account | account_asset
#   order_size broker | asset | account_asset
#   pnl_bounds          asset | account_asset
# windowMs is a positive integer (<= 24h); maxOrders a positive integer; all
# decimal ceilings/bounds are exact strings.
LIMITS = [
    {
        # Burst guard for the runaway-algo desk — at most 10 orders per second.
        # The Infinite Loop Capital exhibit is exactly why this exists.
        "kind": "rate",
        "scope": "account",
        "account": "algo-infinite-loop",
        "asset": "",
        "windowMs": 1000,
        "maxOrders": 10,
    },
    {
        # A saner ceiling for a desk that's still allowed to trade.
        "kind": "rate",
        "scope": "account",
        "account": "algo-null-pointer",
        "asset": "",
        "windowMs": 1000,
        "maxOrders": 50,
    },
    {
        # Global fat-finger ceiling: no single order may exceed 10 000 units or
        # 5 000 000 notional, regardless of account. Broker scope applies to every
        # order that passes through the engine.
        "kind": "order_size",
        "scope": "broker",
        "account": "",
        "asset": "",
        "maxQuantity": "10000",
        "maxNotional": "5000000",
    },
    {
        # Per-name leash on a degen: at most 1 000 AAPL units per order.
        "kind": "order_size",
        "scope": "account_asset",
        "account": "degen-yolo",
        "asset": "AAPL",
        "maxQuantity": "1000",
    },
    {
        # Index notional cap, asset-wide: SPX orders capped at 2 000 000 notional.
        "kind": "order_size",
        "scope": "asset",
        "account": "",
        "asset": "SPX",
        "maxNotional": "2000000",
    },
    {
        # Kill-switch: halt Diamond Hands Capital if realized USD P&L sinks below
        # half a million in the red. They never sell, until they do.
        "kind": "pnl_bounds",
        "scope": "account_asset",
        "account": "degen-diamond-hands",
        "asset": "USD",
        "lowerBound": "-500000",
    },
    {
        # A tighter leash on a known blow-up.
        "kind": "pnl_bounds",
        "scope": "account_asset",
        "account": "blowup-catching-knives",
        "asset": "USD",
        "lowerBound": "-250000",
    },
    {
        # Firm-wide USD P&L floor, asset-wide.
        "kind": "pnl_bounds",
        "scope": "asset",
        "account": "",
        "asset": "USD",
        "lowerBound": "-2000000",
    },
]

# Endpoint and response-key per limit kind.
_LIMIT_ENDPOINTS = {
    "rate": "/limits/rate",
    "order_size": "/limits/order-size",
    "pnl_bounds": "/limits/pnl-bounds",
}
_LIMIT_RESP_KEYS = {
    "rate": "rateLimit",
    "order_size": "orderSizeLimit",
    "pnl_bounds": "pnlBoundsLimit",
}

def _limit_body(limit: dict[str, Any]) -> dict[str, Any]:
    """Build the wire body for a typed limit from its seed entry."""
    body: dict[str, Any] = {
        "scope": limit["scope"],
        "account": limit["account"],
        "asset": limit["asset"],
    }
    kind = limit["kind"]
    if kind == "rate":
        body["windowMs"] = limit["windowMs"]
        body["maxOrders"] = limit["maxOrders"]
    elif kind == "order_size":
        body["maxQuantity"] = limit.get("maxQuantity", "")
        body["maxNotional"] = limit.get("maxNotional", "")
    elif kind == "pnl_bounds":
        body["lowerBound"] = limit.get("lowerBound", "")
        body["upperBound"] = limit.get("upperBound", "")
        if limit.get("initialPnl"):
            body["initialPnl"] = limit["initialPnl"]
    return body


def _limit_label(limit: dict[str, Any]) -> str:
    label = f"{limit['kind']} / {limit['scope']}"
    if limit["account"]:
        label += f" / {limit['account']}"
    if limit["asset"]:
        label += f" / {limit['asset']}"
    return label


def _limit_key(limit: dict[str, Any]) -> tuple[str, str, str, str]:
    return (limit["kind"], limit["scope"], limit["account"], limit["asset"])


def _existing_limit_keys(base: str) -> set[tuple[str, str, str, str]]:
    resp = _get(base, "/limits")
    limits = resp.get("limits", {})
    keys: set[tuple[str, str, str, str]] = set()
    for limit in limits.get("rateLimits", []):
        keys.add(("rate", limit.get("scope", ""), limit.get("account", ""), limit.get("asset", "")))
    for limit in limits.get("orderSizeLimits", []):
        keys.add(
            ("order_size", limit.get("scope", ""), limit.get("account", ""), limit.get("asset", ""))
        )
    for limit in limits.get("pnlBoundsLimits", []):
        keys.add(
            ("pnl_bounds", limit.get("scope", ""), limit.get("account", ""), limit.get("asset", ""))
        )
    return keys


# Orders: account, baseAsset, quoteAsset, side, amountKind, amountValue, price.
# execution_reports: quantity, price, status. The fill's lockPrice is taken from
# the order's settlement-leg display price (the LAST entry of order.displayPrices).
# leavesQuantity (FIX LeavesQty - the order's remaining open base quantity after
# the fill) is computed per fill from the running cumulative filled quantity; it
# is required by the engine to settle. Every filled order here is "quantity" kind,
# so leaves is exact base units; a "volume" order with fills cannot derive
# base-unit leaves and is rejected as a seed error.
# Some orders intentionally trip a limit or a block: the engine returns the order
# in rejected status (still a 201), which is a feature of the demo, not an error.
ORDERS = [
    {
        "account": "whale-bucks",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "500",
        "price": "185.50",
        "execution_reports": [
            {"quantity": "300", "price": "185.40", "status": "partially_filled"},
            {"quantity": "200", "price": "185.50", "status": "filled"},
        ],
    },
    {
        "account": "whale-gravy-train",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "1000",
        "price": "184.20",
        "execution_reports": [
            {"quantity": "1000", "price": "184.10", "status": "filled"},
        ],
    },
    {
        "account": "whale-rocket-surgeons",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "200",
        "price": "415.00",
        "execution_reports": [
            {"quantity": "200", "price": "414.75", "status": "filled"},
        ],
    },
    {
        "account": "degen-diamond-hands",
        "baseAsset": "SPX",
        "quoteAsset": "USD",
        "side": "sell",
        "amountKind": "quantity",
        "amountValue": "10",
        "price": "5200.00",
        "execution_reports": [
            # Partial fill only — still open.
            {"quantity": "4", "price": "5195.00", "status": "partially_filled"},
        ],
    },
    {
        "account": "degen-fomo",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "volume",
        "amountValue": "50000.00",
        "price": "190.00",
        # No execution reports — order accepted, not yet filled.
        "execution_reports": [],
    },
    {
        "account": "degen-hindsight",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "sell",
        "amountKind": "quantity",
        "amountValue": "100",
        "price": "410.00",
        "execution_reports": [
            {"quantity": "100", "price": "409.50", "status": "filled"},
        ],
    },
    {
        # Trips the per-name order-size leash (1 000 AAPL) — rejected on submit.
        "account": "degen-yolo",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "1500",
        "price": "186.00",
        "execution_reports": [],
    },
    {
        # Trips the global fat-finger ceiling (10 000 units) — rejected on submit.
        "account": "degen-revenge-trade",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "50000",
        "price": "185.00",
        "execution_reports": [],
    },
    {
        "account": "degen-leverage-regret",
        "baseAsset": "SPX",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "5",
        "price": "5180.00",
        "execution_reports": [
            {"quantity": "5", "price": "5181.00", "status": "filled"},
        ],
    },
    {
        "account": "algo-null-pointer",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "300",
        "price": "416.00",
        "execution_reports": [
            {"quantity": "300", "price": "415.90", "status": "filled"},
        ],
    },
    {
        "account": "blowup-catching-knives",
        "baseAsset": "SPX",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "3",
        "price": "5000.00",
        "execution_reports": [
            {"quantity": "3", "price": "4990.00", "status": "filled"},
        ],
    },
    # Infinite Loop Capital runaway-algo burst. The desk is blocked, so the
    # engine rejects every order on submit — exactly the cautionary tale the
    # rate-limit barrier exists to prevent.
    {
        "account": "algo-infinite-loop",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "250",
        "price": "187.00",
        "execution_reports": [],
    },
    {
        "account": "algo-infinite-loop",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "180",
        "price": "186.75",
        "execution_reports": [],
    },
    {
        "account": "algo-infinite-loop",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "150",
        "price": "418.50",
        "execution_reports": [],
    },
    {
        "account": "algo-infinite-loop",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "sell",
        "amountKind": "quantity",
        "amountValue": "140",
        "price": "420.00",
        "execution_reports": [],
    },
    {
        "account": "algo-infinite-loop",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "300",
        "price": "185.50",
        "execution_reports": [],
    },
    {
        "account": "algo-infinite-loop",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "500",
        "price": "417.00",
        "execution_reports": [],
    },
]


def _order_key(order: dict[str, Any]) -> tuple[str, str, str, str, str, str, str]:
    return (
        order["account"],
        order["baseAsset"],
        order["quoteAsset"],
        order["side"],
        order["amountKind"],
        order["amountValue"],
        order.get("price", ""),
    )


def _existing_order_keys(base: str) -> set[tuple[str, str, str, str, str, str, str]]:
    resp = _get(base, "/orders?limit=1000")
    keys: set[tuple[str, str, str, str, str, str, str]] = set()
    for order in resp.get("orders", []):
        keys.add(
            (
                order.get("account", ""),
                order.get("baseAsset", ""),
                order.get("quoteAsset", ""),
                order.get("side", ""),
                order.get("amountKind", ""),
                order.get("amountValue", ""),
                order.get("price", ""),
            )
        )
    return keys


# ---------------------------------------------------------------------------
# Main seed logic
# ---------------------------------------------------------------------------


def seed(base: str) -> None:
    assets_created = 0
    groups_created = 0
    accounts_created = 0
    limits_created = 0
    orders_submitted = 0
    trades_created = 0
    errors: list[str] = []

    # --- Asset classes --------------------------------------------------------
    # Created before assets: an asset links to its class by a foreign key, so the
    # class code must already exist or the asset create is rejected.
    print("\n=== Asset classes ===")
    for cls in ASSET_CLASSES:
        resp = _post(
            base,
            "/asset-classes",
            {"code": cls["code"], "title": cls["title"], "notes": cls["notes"]},
        )
        if "assetClass" in resp:
            print(f"  [+] asset class '{cls['code']}'")
        else:
            code = resp.get("error", {}).get("code", "")
            if code == "conflict":
                print(f"  [=] asset class '{cls['code']}' already exists")
            else:
                msg = f"asset class '{cls['code']}': {resp}"
                print(f"  [!] {msg}", file=sys.stderr)
                errors.append(msg)

    # --- Assets ---------------------------------------------------------------
    print("\n=== Assets ===")
    for asset in ASSETS:
        resp = _post(
            base,
            "/assets",
            {
                "code": asset["code"],
                "title": asset["title"],
                "assetClass": asset["assetClass"],
            },
        )
        if "asset" in resp:
            print(f"  [+] asset '{asset['code']}'")
            assets_created += 1
        else:
            code = resp.get("error", {}).get("code", "")
            if code == "conflict":
                print(f"  [=] asset '{asset['code']}' already exists")
            else:
                msg = f"asset '{asset['code']}': {resp}"
                print(f"  [!] {msg}", file=sys.stderr)
                errors.append(msg)

    # --- Groups ---------------------------------------------------------------
    print("\n=== Groups ===")
    for g in GROUPS:
        resp = _post(base, "/groups", {"code": g["code"], "title": g["title"], "notes": g["notes"]})
        if "group" in resp:
            print(f"  [+] group '{g['code']}'")
            groups_created += 1
        else:
            code = resp.get("error", {}).get("code", "")
            if code == "conflict":
                print(f"  [=] group '{g['code']}' already exists")
            else:
                msg = f"group '{g['code']}': {resp}"
                print(f"  [!] {msg}", file=sys.stderr)
                errors.append(msg)

    # --- Accounts -------------------------------------------------------------
    print("\n=== Accounts ===")
    for acct in ACCOUNTS:
        acct_id: str = acct["code"]
        url_id = urllib.parse.quote(acct_id, safe="")

        # Create
        resp = _post(base, "/accounts", {"code": acct_id, "title": acct["title"]})
        if "account" in resp:
            print(f"  [+] account '{acct_id}'")
            accounts_created += 1
        else:
            code = resp.get("error", {}).get("code", "")
            if code == "conflict":
                print(f"  [=] account '{acct_id}' already exists")
                continue
            else:
                msg = f"create account '{acct_id}': {resp}"
                print(f"  [!] {msg}", file=sys.stderr)
                errors.append(msg)
                continue

        # Group
        if acct.get("group"):
            _put(base, f"/accounts/{url_id}/group", {"group": acct["group"]})

        # Notes
        if acct.get("notes"):
            _put(base, f"/accounts/{url_id}/notes", {"notes": acct["notes"]})

        # Balances
        for adj in acct.get("balances", []):
            body: dict[str, Any] = {"asset": adj["asset"]}
            if "balance" in adj:
                body["balance"] = adj["balance"]
            if "held" in adj:
                body["held"] = adj["held"]
            adj_resp = _post(base, f"/accounts/{url_id}/adjustments", body, conflict_ok=False)
            adj_record = adj_resp.get("adjustment", {})
            status = adj_record.get("status", "?")
            print(f"    adjustment {adj['asset']}: {status}")

        # Block (after balance so the block is the last state)
        if acct.get("blocked"):
            reason: str = acct.get("block_reason", "Demo block.")
            _post(base, f"/accounts/{url_id}/block", {"reason": reason}, conflict_ok=False)
            print(f"    blocked: {reason}")

    # --- Limits ---------------------------------------------------------------
    print("\n=== Limits ===")
    try:
        existing_limits = _existing_limit_keys(base)
    except Exception as exc:
        existing_limits = set()
        msg = f"list limits: {exc}"
        print(f"  [!] {msg}", file=sys.stderr)
        errors.append(msg)
    for limit in LIMITS:
        label = _limit_label(limit)
        if _limit_key(limit) in existing_limits:
            print(f"  [=] {label} already exists")
            continue
        resp_key = _LIMIT_RESP_KEYS[limit["kind"]]
        try:
            resp = _put(base, _LIMIT_ENDPOINTS[limit["kind"]], _limit_body(limit))
            if resp_key in resp:
                print(f"  [+] {label}")
                limits_created += 1
            else:
                msg = f"put limit {label}: {resp}"
                print(f"  [!] {msg}", file=sys.stderr)
                errors.append(msg)
        except Exception as exc:
            msg = f"put limit {label}: {exc}"
            print(f"  [!] {msg}", file=sys.stderr)
            errors.append(msg)

    # --- Orders + execution reports ------------------------------------------
    print("\n=== Orders ===")
    try:
        existing_orders = _existing_order_keys(base)
    except Exception as exc:
        existing_orders = set()
        msg = f"list orders: {exc}"
        print(f"  [!] {msg}", file=sys.stderr)
        errors.append(msg)
    for order_def in ORDERS:
        acct_id = order_def["account"]
        instrument = f"{order_def['baseAsset']}/{order_def['quoteAsset']}"
        if _order_key(order_def) in existing_orders:
            print(f"  [=] order {instrument} for '{acct_id}' already exists")
            continue

        body = {
            "account": acct_id,
            "baseAsset": order_def["baseAsset"],
            "quoteAsset": order_def["quoteAsset"],
            "side": order_def["side"],
            "amountKind": order_def["amountKind"],
            "amountValue": order_def["amountValue"],
        }
        if "price" in order_def:
            body["price"] = order_def["price"]

        resp = _post(base, "/orders", body, conflict_ok=False)
        order = resp.get("order", {})
        order_id = order.get("externalId")
        status = order.get("status", "?")

        if order_id is None:
            msg = f"submit order {instrument} for '{acct_id}': {resp}"
            print(f"  [!] {msg}", file=sys.stderr)
            errors.append(msg)
            continue

        print(f"  [+] order {order_id}  {instrument}  {order_def['side']}  status={status}")
        orders_submitted += 1

        if status == "rejected":
            if order_def.get("execution_reports"):
                print("      fills skipped: order rejected")
            continue

        # The settlement-leg lock price is the LAST entry of displayPrices; it is
        # the price the engine locked the reservation at and what a fill settles to.
        display_prices: list[str] = order.get("displayPrices", [])
        lock_price: str | None = display_prices[-1] if display_prices else None

        fills = order_def.get("execution_reports", [])
        # leavesQuantity is base-unit remaining; we can only derive it for a
        # quantity-kind order. A volume order with fills would need a per-fill
        # base size the seed does not carry, so it is a defect, not demo data.
        if fills and order_def["amountKind"] != "quantity":
            msg = (
                f"order {instrument} for '{acct_id}': cannot derive base-unit "
                f"leavesQuantity for amountKind={order_def['amountKind']}"
            )
            print(f"  [!] {msg}", file=sys.stderr)
            errors.append(msg)
            continue

        order_url_id = urllib.parse.quote(order_id, safe="")
        order_quantity = Decimal(order_def["amountValue"])
        cumulative_filled = Decimal(0)
        for er in fills:
            cumulative_filled += Decimal(er["quantity"])
            leaves = order_quantity - cumulative_filled
            er_body: dict[str, Any] = {
                "quantity": er["quantity"],
                "price": er["price"],
                "leavesQuantity": str(leaves),
                "status": er["status"],
            }
            if lock_price is not None:
                er_body["lockPrice"] = lock_price

            er_resp = _post(
                base, f"/orders/{order_url_id}/execution-reports", er_body, conflict_ok=False
            )
            result = er_resp.get("result", {})
            blocks = result.get("blocks", [])
            outcomes = result.get("outcomes", [])
            # A normal fill must settle without blocking the account; a block here
            # means the fill itself was rejected, which is a real defect.
            if blocks:
                reasons = ", ".join(b.get("reason", b.get("code", "?")) for b in blocks)
                msg = (
                    f"order {instrument} for '{acct_id}': fill qty={er['quantity']} "
                    f"blocked the account: {reasons}"
                )
                print(f"  [!] {msg}", file=sys.stderr)
                errors.append(msg)
                continue
            status_tag = f" [{er['status']}]"
            print(
                f"      fill qty={er['quantity']} px={er['price']}"
                f" leaves={leaves} outcomes={len(outcomes)}{status_tag}"
            )
            trades_created += 1

    # --- Summary --------------------------------------------------------------
    print("\n=== Seed complete ===")
    print(f"  Assets   created               : {assets_created}")
    print(f"  Groups   created               : {groups_created}")
    print(f"  Accounts created               : {accounts_created}")
    print(f"  Limits   created               : {limits_created}")
    print(f"  Orders submitted               : {orders_submitted}")
    print(f"  Fills posted                   : {trades_created}")
    if errors:
        print(f"\n  Errors ({len(errors)}):")
        for e in errors:
            print(f"    - {e}")
        sys.exit(1)


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------


def _parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Seed a running Pit Officer instance with demo data.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument(
        "--base",
        metavar="URL",
        default=None,
        help="Base URL of the Officer service (e.g. http://127.0.0.1:8080). "
        "Overrides auto-discovery.",
    )
    parser.add_argument(
        "--runtime-file",
        metavar="PATH",
        default=None,
        help="Path to officer-runtime.json. Used when auto-discovery cannot "
        "find the file in the default locations.",
    )
    return parser.parse_args()


def main() -> None:
    args = _parse_args()
    base = _discover_base(args.base, args.runtime_file)
    print(f"Seeding {base} ...")
    seed(base)


if __name__ == "__main__":
    main()
