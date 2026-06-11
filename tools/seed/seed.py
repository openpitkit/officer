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
# Demo data only — all account names are fictional parodies of archetypes;
# any resemblance to real persons or entities is unintended.

"""Pit Officer demo seed utility.

Populates a running Pit Officer instance via its public REST API (/api/v1)
with a set of fictional accounts, groups, balances, orders, and trades so the
team can take demo screenshots and sanity-check behaviour.

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
import os
import sys
import urllib.error
import urllib.parse
import urllib.request
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


# ---------------------------------------------------------------------------
# Seed data
# ---------------------------------------------------------------------------

GROUPS = [
    {
        "id": "whales",
        "notes": "Accounts with enough capital to move markets — handle with care.",
    },
    {
        "id": "degens",
        "notes": "High-frequency risk-takers; frequent flyers on the margin call list.",
    },
    {
        "id": "blowups",
        "notes": "Accounts currently underwater. Do not lend them more rope.",
    },
]

# Each account: id, group, notes, balances (list of adjustment dicts), blocked/reason.
# Balance adjustments use mode "set" on the `balance` field (absolute USD value).
# For the underwater desk we also set a large negative `held` to model locked losses.
ACCOUNTS = [
    {
        "id": "Bucks McMoneyface",
        "group": "whales",
        "notes": "Patriarch of the McMoneyface dynasty. Buys dips; is the dip.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "12500000.00"}},
        ],
        "blocked": False,
    },
    {
        "id": "Rocket Surgeons LLC",
        "group": "whales",
        "notes": "Technically we do rocket surgery. Financially, same thing.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "9800000.00"}},
        ],
        "blocked": False,
    },
    {
        "id": "Diamond Hands Capital",
        "group": "degens",
        "notes": "We never sell. Not once. Not ever. (We always sell at the bottom.)",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "3200000.00"}},
        ],
        "blocked": False,
    },
    {
        "id": "FOMO Ventures",
        "group": "degens",
        "notes": "Late to every trade since inception. Holding the bag professionally.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "750000.00"}},
        ],
        "blocked": False,
    },
    {
        "id": "Hindsight Asset Mgmt",
        "group": "degens",
        "notes": "Our research is flawless — six months after the fact.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "1100000.00"}},
        ],
        "blocked": False,
    },
    {
        "id": "Margin Call Partners",
        "group": "blowups",
        "notes": "Partners since 2019. Still partners. Margin desk is not a partner.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "85000.00"}},
            # Held models outstanding reserve from underwater positions.
            {"asset": "USD", "held": {"mode": "absolute", "value": "-240000.00"}},
        ],
        "blocked": False,
    },
    {
        "id": "Buy High Sell Low Inc",
        "group": "blowups",
        "notes": "Strategy document available on request. Results as advertised.",
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "42000.00"}},
            {"asset": "USD", "held": {"mode": "absolute", "value": "-110000.00"}},
        ],
        "blocked": False,
    },
    {
        "id": "Lemon Brothers",
        "group": "blowups",
        "notes": (
            "Legendary desk, historically significant collapse. "
            "Kept as a cautionary exhibit. Do not extend credit."
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
    {
        "id": "Knightmare Capital",
        "group": "blowups",
        "notes": (
            "Runaway algo exhibit — automated order engine spun out of control. "
            "Generated massive order flow in minutes before halt. Cautionary tale for pre-trade risk."
        ),
        "balances": [
            {"asset": "USD", "balance": {"mode": "absolute", "value": "500000.00"}},
        ],
        "blocked": True,
        "block_reason": "Trading halt — runaway algorithm kill-switch activated.",
    },
]

# Orders: account, baseAsset, quoteAsset, side, amountKind, amountValue, price.
# execution_reports: quantity, price, final.  lockPrice is taken from order.lockPrices[0].
ORDERS = [
    {
        "account": "Bucks McMoneyface",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "500",
        "price": "185.50",
        "execution_reports": [
            {"quantity": "300", "price": "185.40", "final": False},
            {"quantity": "200", "price": "185.50", "final": True},
        ],
    },
    {
        "account": "Rocket Surgeons LLC",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "200",
        "price": "415.00",
        "execution_reports": [
            {"quantity": "200", "price": "414.75", "final": True},
        ],
    },
    {
        "account": "Diamond Hands Capital",
        "baseAsset": "SPX",
        "quoteAsset": "USD",
        "side": "sell",
        "amountKind": "quantity",
        "amountValue": "10",
        "price": "5200.00",
        "execution_reports": [
            # Partial fill only — still open.
            {"quantity": "4", "price": "5195.00", "final": False},
        ],
    },
    {
        "account": "FOMO Ventures",
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
        "account": "Hindsight Asset Mgmt",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "sell",
        "amountKind": "quantity",
        "amountValue": "100",
        "price": "410.00",
        "execution_reports": [
            {"quantity": "100", "price": "409.50", "final": True},
        ],
    },
    # Knightmare Capital runaway algo burst (rapid-fire orders)
    {
        "account": "Knightmare Capital",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "250",
        "price": "187.00",
        "execution_reports": [],
    },
    {
        "account": "Knightmare Capital",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "180",
        "price": "186.75",
        "execution_reports": [],
    },
    {
        "account": "Knightmare Capital",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "150",
        "price": "418.50",
        "execution_reports": [],
    },
    {
        "account": "Knightmare Capital",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "120",
        "price": "418.00",
        "execution_reports": [],
    },
    {
        "account": "Knightmare Capital",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "sell",
        "amountKind": "quantity",
        "amountValue": "200",
        "price": "188.00",
        "execution_reports": [],
    },
    {
        "account": "Knightmare Capital",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "sell",
        "amountKind": "quantity",
        "amountValue": "140",
        "price": "420.00",
        "execution_reports": [],
    },
    {
        "account": "Knightmare Capital",
        "baseAsset": "AAPL",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "300",
        "price": "185.50",
        "execution_reports": [],
    },
    {
        "account": "Knightmare Capital",
        "baseAsset": "MSFT",
        "quoteAsset": "USD",
        "side": "buy",
        "amountKind": "quantity",
        "amountValue": "500",
        "price": "417.00",
        "execution_reports": [],
    },
]


# ---------------------------------------------------------------------------
# Main seed logic
# ---------------------------------------------------------------------------


def seed(base: str) -> None:
    groups_created = 0
    accounts_created = 0
    orders_submitted = 0
    trades_created = 0
    errors: list[str] = []

    # --- Groups ---------------------------------------------------------------
    print("\n=== Groups ===")
    for g in GROUPS:
        resp = _post(base, "/groups", {"id": g["id"], "notes": g["notes"]})
        if "group" in resp:
            print(f"  [+] group '{g['id']}'")
            groups_created += 1
        else:
            code = resp.get("error", {}).get("code", "")
            if code == "conflict":
                print(f"  [=] group '{g['id']}' already exists")
            else:
                msg = f"group '{g['id']}': {resp}"
                print(f"  [!] {msg}", file=sys.stderr)
                errors.append(msg)

    # --- Accounts -------------------------------------------------------------
    print("\n=== Accounts ===")
    for acct in ACCOUNTS:
        acct_id: str = acct["id"]
        url_id = urllib.parse.quote(acct_id, safe="")

        # Create
        resp = _post(base, "/accounts", {"id": acct_id})
        if "account" in resp:
            print(f"  [+] account '{acct_id}'")
            accounts_created += 1
        else:
            code = resp.get("error", {}).get("code", "")
            if code == "conflict":
                print(f"  [=] account '{acct_id}' already exists")
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

    # --- Orders + execution reports ------------------------------------------
    print("\n=== Orders ===")
    for order_def in ORDERS:
        acct_id = order_def["account"]

        body: dict[str, Any] = {
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
        order_id = order.get("id")
        status = order.get("status", "?")
        instrument = f"{order_def['baseAsset']}/{order_def['quoteAsset']}"

        if order_id is None:
            msg = f"submit order {instrument} for '{acct_id}': {resp}"
            print(f"  [!] {msg}", file=sys.stderr)
            errors.append(msg)
            continue

        print(f"  [+] order #{order_id}  {instrument}  {order_def['side']}  status={status}")
        orders_submitted += 1

        # lockPrices[0] is the price the engine locked funds at.
        lock_prices: list[str] = order.get("lockPrices", [])
        lock_price: str | None = lock_prices[0] if lock_prices else None

        for er in order_def.get("execution_reports", []):
            er_body: dict[str, Any] = {
                "quantity": er["quantity"],
                "price": er["price"],
                "final": er["final"],
            }
            if lock_price is not None:
                er_body["lockPrice"] = lock_price

            er_resp = _post(base, f"/orders/{order_id}/execution-reports", er_body, conflict_ok=False)
            result = er_resp.get("result", {})
            blocks = result.get("blocks", [])
            outcomes = result.get("outcomes", [])
            final_tag = " [final]" if er["final"] else ""
            block_tag = f" BLOCKS={len(blocks)}" if blocks else ""
            print(
                f"      fill qty={er['quantity']} px={er['price']}"
                f" outcomes={len(outcomes)}{block_tag}{final_tag}"
            )
            trades_created += 1

    # --- Summary --------------------------------------------------------------
    print("\n=== Seed complete ===")
    print(f"  Groups   created : {groups_created}")
    print(f"  Accounts created : {accounts_created}")
    print(f"  Orders submitted : {orders_submitted}")
    print(f"  Fills posted     : {trades_created}")
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
