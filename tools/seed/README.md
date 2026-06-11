# Officer demo seed

Populates a running Pit Officer instance with fictional demo accounts, groups,
balances, orders, and execution reports via the public REST API (`/api/v1`).

## Requirements

Python 3.11+ — standard library only, no pip installs.

## Usage

```sh
# Auto-discover the running server and seed it:
python tools/seed/seed.py

# Explicit base URL:
python tools/seed/seed.py --base http://127.0.0.1:8080

# Point to a specific runtime file:
python tools/seed/seed.py --runtime-file /path/to/officer-runtime.json
```

Or via `just` from the `officer/` directory:

```sh
just seed
```

## Base-URL discovery

The script tries the following in order:

1. `--base` flag (explicit override).
2. `officer-runtime.json` next to `seed.py` (auto-written by `pit-officer serve`).
3. `officer-runtime.json` in the working directory.
4. Error — prints a message to stderr and exits non-zero. Pass `--base` or start `serve`.

## What gets created

| Account | Group | Highlights |
|---|---|---|
| Bucks McMoneyface | whales | Healthy whale; $12.5M balance |
| Rocket Surgeons LLC | whales | Healthy whale; $9.8M balance |
| Diamond Hands Capital | degens | Mid-size; partial fill left open |
| FOMO Ventures | degens | Order accepted, unfilled |
| Hindsight Asset Mgmt | degens | Normal desk; filled sell order |
| Margin Call Partners | blowups | Underwater; negative held |
| Buy High Sell Low Inc | blowups | Underwater; negative held |
| Lemon Brothers | blowups | **Blocked**; catastrophic −$47.3M held |
| Knightmare Capital | blowups | **Blocked**; runaway algo burst (8 orders); $500K balance |

Groups: `whales`, `degens`, `blowups`.

Instruments used: `AAPL/USD`, `MSFT/USD`, `SPX/USD` (non-crypto equities/index).

The script is idempotent-friendly: HTTP 409 (already exists) is treated as OK
and the run continues.
