# Execution Simulator

## What it is

Execution Simulator is a paper-only venue with provider ID `test`. It needs no
keys and sends no requests to an external venue. It fills paper orders through
the ordinary send path according to its execution policy, using current quotes.
Use it for strategy and AI-agent forward
testing without a venue account.

## Setup from an empty Officer

Start Officer and finish its first-run setup as described in the
[quickstart](quickstart.md). The examples use multi-account mode; see
[Single-account mode](api.md#single-account-mode) for that mode's behavior.

REST paths below are relative to `/api/v1` unless a full mount path is shown.
Authentication and the panel mirror are described in
[Mount points](api.md#mount-points). Replace `CONNECTION_ID`, `ACCOUNT_CODE` and
`INSTANCE_ID` with the saved connection ID, test account code and market-data
instance ID. Replace `ORDER_ID` with a fresh order ID for each new order; it is
not a credential.

The operator prepares the following before an agent sends orders:

1. Create a dedicated test account, or a dedicated account group and its member
   accounts. Set the account currency and fund the account with paper balances.
   The scenarios below use `BTC/USD`. Create the account with `POST /accounts`:

   ```json
   {"code": "ACCOUNT_CODE", "currency": "USD"}
   ```

   Fund it before trading setup with
   `POST /accounts/ACCOUNT_CODE/adjustments?missingAccount=reject`:

   ```json
   {
     "type": "deposit",
     "asset": "USD",
     "balance": {"mode": "absolute", "value": "100000"}
   }
   ```

   Both calls answer `201`.
   An account may access both paper and live connections and any provider kinds.
   Effective access includes its group grants and disabled connections. A change
   that creates mixed paper/live access publishes a warning naming the account;
   the panel also shows the current warning beside its venue-access count and
   in its access dialog. Acknowledgement changes no grants or orders. Check the
   destination mode before sending an order.
2. Create a disabled connection with provider `test`, mode `paper` and empty
   credentials. Save its returned `connection.id`.
3. Add a test execution rule for base `BTC`, quote `USD`, or use masks such as
   `*/USD`. Test execution needs no external symbol mapping.
4. Grant the test account, or its group, access to that connection.
5. Configure a market-data source with fresh bid and ask for the pair. In the
   panel, select **Add source** > **Mock**, label it `Synthetic quotes` and
   enable it. Mock supplies synthetic market data for demos and local testing;
   it does not connect to an external venue. Add an enabled instrument with
   external symbol `BTC/USD`, base asset `BTC`, quote asset `USD` and an empty
   manual price, then select **Restart feeds**.

   Over REST, create the source with `POST /market-data/instances`:

   ```json
   {"provider": "mock", "label": "Synthetic quotes", "enabled": true}
   ```

   It answers `201`; save `instance.id` as `INSTANCE_ID`. Add the instrument
   with `PUT /market-data/instances/INSTANCE_ID/instruments`:

   ```json
   {
     "externalSymbol": "BTC/USD",
     "baseAsset": "BTC",
     "quoteAsset": "USD",
     "manualPrice": "",
     "enabled": true
   }
   ```

   Call `POST /market-data/restart`, then read `GET /market-data`. Find the
   instance and instrument in `marketData.instances[].instruments[]` and read
   its `quote`, including `bid`, `ask`, `mark` and `asOf`. Mock prices are
   synthetic and move; read the served quote before comparing a fill. Another
   source that supplies fresh bid and ask works through the same quote path.

   Keep exactly one enabled direct market-data instrument row for `BTC/USD`,
   disable extra rows or instances, then restart feeds. Direct records take
   precedence over inverse records; an inverse is considered only when no direct
   record exists. Several direct records refuse with `test_quote_ambiguous`, even
   if one arrived later. Count instrument rows, not just instances. A manual
   mark alone does not supply bid or ask for market execution.
6. Save an execution policy as described below, then enable the connection.
   On a non-release build,
   `PUT /api/v1/trading/connections/CONNECTION_ID/enabled` with
   `{"enabled": true}` answers `403` `non_release_confirmation_required`.
   In the panel, select **Enable trading** in the warning dialog. The panel
   mirror call is
   `PUT /app/api/v1/trading/connections/CONNECTION_ID/enabled`:

   ```json
   {"enabled": true, "nonReleaseConfirmed": true}
   ```

   It answers `200`. Wait until `GET /trading` shows `state: "running"` for
   the connection in `trading.connections[]`. For a release build, follow
   [Trading](api.md#trading); release enablement was not run here. Setup is an
   operator action: enabling MCP tools does not let the agent create or enable
   this connection or grant access.

Create the connection with `POST /trading/connections`:

```json
{
  "provider": "test",
  "label": "Strategy test",
  "mode": "paper",
  "credentials": {},
  "enabled": false
}
```

Test connections use the execution policy's symbol masks directly, without a
trading instrument mapping. Market-data sources still need their own mappings.

Grant access with `PUT /trading/connections/CONNECTION_ID/access`:

```json
{"account": "ACCOUNT_CODE"}
```

In single-account mode, creation and enabling retain the normal automatic scope
grant, including when it creates mixed paper/live access. If warning publication
or delivery fails after persistence, `trading_access_notification_failed` says
the access change was saved. Review the current access and alerts before retrying.

### From the panel

- Open **Trading connections** > **Add trading connection** > **Execution Simulator**. Enter a
  label; there are no key fields or trading-mode selector.
- On the connection card, open **Test execution policy**, select **Add rule**,
  fill the rule and select **Save**. **Saved revision** shows the saved revision
  number. A refused save shows the server error inside this editor, just above
  **Add rule** and **Save**; the saved revision is not advanced by validation
  refusal.
- Select **Check connection**. A successful check answers: "Check passed: test
  execution reaches no venue and needs no keys." This checks the connection;
  it does not demonstrate that an order has a matching rule and a usable quote.

Use **Delete trading connection** on the card to remove a disabled connection
with no venue-order history. The confirmation removes its credentials,
instruments, execution policy and access grants. A connection referenced by any
venue order is retained so its order history remains readable. The equivalent
API operation is `DELETE /trading/connections/CONNECTION_ID`.

## The execution policy

A new test connection has revision `"1"` and an explicitly empty `rules: []`.
An order under that policy is refused with `test_policy_no_match`.

### Masks and selection

Each rule has separate `mask.baseAsset` and `mask.quoteAsset` masks, matched
against Officer asset codes. Matching is case-sensitive and covers the whole
code. Only `*` is a wildcard meaning any sequence; every other character is
literal.

Examples: `*` matches all codes, `BTC` matches only `BTC`, and `USD*` matches
`USD`, `USDT` and `USDC`.

For example, an `execute` rule with base `BTC`, quote `USD`, variant `normal`,
and another `execute` rule with base `B*`, quote `USD`, variant `best`, both
match `BTC/USD`. The first rule wins and uses `normal`. Reversing the rules
selects `best`. Rules are checked from top to bottom; use **Higher priority**
and **Lower priority** in the panel, then **Save**, to change their order.
No match refuses the order as an unknown symbol with `test_policy_no_match`.

### Modes

- `execute` fills the full quantity once.
- `split` requires `fills: 2` or `fills: 3`. The incoming quantity string's
  decimal precision, including trailing zeros, sets the splitting unit.
  `1.00` splits into `0.50 + 0.50`, or `0.34 + 0.33 + 0.33`. With three fills,
  `1` and `0.02` refuse with `test_split_unrepresentable`. The whole plan must
  be representable before any fill; all fills use one price snapshot.
- `reject_even` executes the first new order in this mode, refuses the second
  with `test_policy_even_rejected`, and repeats. The counter is shared by all
  `reject_even` rules, accounts and instruments of one connection and saved
  revision in one runtime. It counts new accepted request keys in this mode,
  not trades or orders in other modes. Retries, lookups, no-match,
  invalid quantities and missing asset identities do not advance it. An odd
  request that reaches price selection but lacks a quote side does count.
  Every successful policy replacement, even an identical one, and every
  runtime restart start a new sequence with the first order.
- `rest` keeps the order open. A finite wait uses
  `wait: {"kind":"finite","seconds":10}`; `seconds` must be an integer from
  `1` to `9223372036` inclusive. It fills no earlier than acceptance plus the
  delay, selecting the quote at that later time. An infinite wait uses
  `wait: {"kind":"infinite"}` with neither `seconds` nor `priceVariant`.
  It never executes automatically.

Finite waits register an ordinary `Once` job of application kind
`test_execution` on Officer's shared scheduler, with a deadline of acceptance
time plus `seconds`. The worker checks deadlines once per second and executes
due jobs sequentially; execution can be later when other jobs are ahead of it,
but never earlier. Only an execution ID is persisted. Its live test-order
handler is withdrawn on cancellation or a runtime stop. A job whose test order
book no longer exists records a skipped occurrence with reason
`test_order_unavailable`; it never reconstructs or executes an order. There is
no timer or goroutine per order and no separate scheduling queue.

### Prices and the send path

`priceVariant` is required for `execute`, `split`, `reject_even` and finite
`rest`. For market orders:

| Variant | Buy | Sell |
| --- | --- | --- |
| `normal` | Ask | Bid |
| `best` | Bid | Ask |
| `worst` | Ask | Bid |

`normal` and `worst` intentionally coincide. A limit order always fills at its
own `price`, for every variant, even without a quote; this provider does not
wait for the market to cross a limit price. Immediate modes select the price
once before the first fill; finite `rest` selects it at the deadline.

Test orders use the ordinary check, hold and paper-send path. They require
`amountKind: "quantity"` and `destination.route: {}`. Volume orders refuse
before an order is created with `422`, code `validation`, pointer `/amountKind`,
constraint `enum`. Test fills carry no commission.

### Revisions and errors

`PUT /trading/connections/{id}/test-policy` replaces the entire list. Every
successful write returns `connection`, `revision` as a decimal string, and
exactly the saved `rules`; even an unchanged list advances the revision.
`rules` is required, cannot be `null`, and can be `[]`. Existing resting orders
keep the policy snapshot they accepted.

An invalid policy leaves the saved state unchanged. REST semantic validation
answers Problem Details; for the two policy MCP tools, `isError: true` carries
the same public error body in `structuredContent`, including validation
`pointer` and `constraint`. Pointers start at `/rules`, not `/policy/rules`.
Using either policy operation on a venue connection answers
`409` `test_connection_required`. See [Errors](api.md#errors).

## Step by step over MCP

The operator enables `set_test_execution_policy` and `send_paper_order` by name.
Both mutate state and ship disabled. Call
`PUT /mcp-access/set_test_execution_policy` and
`PUT /mcp-access/send_paper_order`, each with this body:

```json
{"enabled": true}
```

Both calls answer `200`. By default, the MCP `grant_trading_access` tool returns
a tool error saying it is disabled by the operator in the Pit Officer panel.
Even when enabled, it returns `loosening_refused`; the operator grants access
during setup. See
[Trading](api.md#trading). Read tools ship enabled. Follow
[MCP tools](api.md#mcp-tools) for this rule. MCP calls below show the `name` and
`arguments` passed to `tools/call`, not a complete JSON-RPC envelope.

### Read the policy

```json
{
  "name": "get_test_execution_policy",
  "arguments": {"connection": "CONNECTION_ID"}
}
```

REST equivalent: `GET /trading/connections/CONNECTION_ID/test-policy`, no body.
Before any policy save, the result is:

```json
{"connection": "CONNECTION_ID", "revision": "1", "rules": []}
```

### Set worst, then send

```json
{
  "name": "set_test_execution_policy",
  "arguments": {
    "connection": "CONNECTION_ID",
    "policy": {
      "rules": [{
        "mask": {"baseAsset": "BTC", "quoteAsset": "USD"},
        "mode": "execute",
        "priceVariant": "worst"
      }]
    }
  }
}
```

For this and each subsequent policy set, the REST equivalent is
`PUT /trading/connections/CONNECTION_ID/test-policy` with exactly the object
inside `arguments.policy` as the body. MCP success returns the saved state in
`structuredContent`; the REST response carries that same state.

Send a market quantity order:

```json
{
  "name": "send_paper_order",
  "arguments": {
    "id": "ORDER_ID",
    "account": "ACCOUNT_CODE",
    "baseAsset": "BTC",
    "quoteAsset": "USD",
    "side": "buy",
    "amountKind": "quantity",
    "amountValue": "1.00",
    "destination": {"connection": "CONNECTION_ID", "route": {}}
  }
}
```

REST equivalent: `POST /orders/send-paper` with `arguments` as the body.
Neither this endpoint nor `send_paper_order` takes `missingAccount`: the account
must already exist and have access to the connection. An unknown account answers
`403` `forbidden` and is not created, even with `?missingAccount=create`.

The numeric examples use bid `99` and ask `101` from the controlled fixture;
they are not promised Mock prices. With these fresh quotes, this worst buy
fills at `101` and a worst sell fills at `99`. With Mock or any live source,
read the served quote before comparing a fill.
The initial send result's `venueSend.status: "acknowledged"` is an
acknowledgement, not proof that the fill has been applied. Read the order.

### Set best

```json
{
  "name": "set_test_execution_policy",
  "arguments": {
    "connection": "CONNECTION_ID",
    "policy": {
      "rules": [{
        "mask": {"baseAsset": "BTC", "quoteAsset": "USD"},
        "mode": "execute",
        "priceVariant": "best"
      }]
    }
  }
}
```

Send the same quantity request with a new order ID. With bid `99` and ask
`101`, a best buy fills at `99` and a best sell fills at `101`, as shown in
the price table. Quotes must still be fresh when execution selects them.

### Reject every second order

```json
{
  "name": "set_test_execution_policy",
  "arguments": {
    "connection": "CONNECTION_ID",
    "policy": {
      "rules": [{
        "mask": {"baseAsset": "BTC", "quoteAsset": "USD"},
        "mode": "reject_even",
        "priceVariant": "normal"
      }]
    }
  }
}
```

Send two new quantity orders with distinct IDs and usable quotes. The first
fills once. The second has `venueSend.status: "refused"` and
`venueSend.refusal.code: "test_policy_even_rejected"`, and creates no trade.
Do not save the policy between those sends: a save restarts the sequence.

### Rest for ten seconds

```json
{
  "name": "set_test_execution_policy",
  "arguments": {
    "connection": "CONNECTION_ID",
    "policy": {
      "rules": [{
        "mask": {"baseAsset": "BTC", "quoteAsset": "USD"},
        "mode": "rest",
        "priceVariant": "normal",
        "wait": {"kind": "finite", "seconds": 10}
      }]
    }
  }
}
```

Send another new quantity order. It stays open without trades until the wait
expires, then uses the quote available at execution time. Saving `execute`
with variant `best` while it waits affects new orders; the resting order
still uses its saved finite wait and `normal` variant.

### Read orders, trades and account state

Read the submitted order with MCP `get_order`, using the submitted order ID.
REST equivalent: `GET /orders/ORDER_ID`. Both reads expose `order.status` and
`trades`; REST also returns `events`. Complete execution reaches `filled`. For
account-wide
reads, use the ordinary `list_orders`, `list_trades` and `list_accounts` tools
and their documented [list filters](api.md#list-tools).

REST equivalents are `GET /orders?account=ACCOUNT_CODE&limit=1000`,
`GET /trades?account=ACCOUNT_CODE&limit=1000`, and `GET /accounts?limit=1000`.
Read `GET /accounts/ACCOUNT_CODE` for the account and
`GET /balances?account=ACCOUNT_CODE&limit=1000` for its balances and positions.

## Failure paths

For an immediate missing-side example, use an `execute` market order with
variant `best` and a current mark-only quote. The send result still has an
approval verdict; inspect `venueSend`, not just that verdict or MCP `isError`.
These are selected fields from the send result:

```json
{
  "venueSend": {
    "status": "refused",
    "refusal": {
      "code": "test_quote_side_missing",
      "baseAsset": "BTC",
      "quoteAsset": "USD",
      "ruleIndexes": [0]
    }
  }
}
```

The REST send answers `201` for this accepted send result; through MCP the
result is in `structuredContent` and is not a tool error. The full refusal also
carries a human-readable `message`; `error` and `venueOrder` are members of
`venueSend`, not of the top-level send result.
Read the order until it is `cancelled`: its `trades` is `[]`, and accounting
does not gain a fill.

For a delayed failure, set the ten-second finite `rest` rule, send an order,
then remove the instrument row before the deadline with
`DELETE /market-data/instances/INSTANCE_ID/instruments?externalSymbol=BTC%2FUSD`
and call `POST /market-data/restart`. The initial send is acknowledged.
At the deadline the order is cancelled without a trade. Read
`GET /trading`, or MCP `get_trading` with empty arguments, and find the
connection's `diagnostics` entry with these selected machine-readable fields:

```json
{"code": "test_quote_missing", "instrument": "BTC/USD"}
```

The diagnostic identifies `BTC/USD` in `instrument`. Read `GET /orders/ORDER_ID`
for the ordinary final state; selected fields are:

```json
{"order": {"status": "cancelled"}, "trades": []}
```

The delayed reason is in connection diagnostics, not the order history.
Held and incoming amounts are released; balances and positions receive no
trade. A quote that becomes stale during the wait similarly refuses with
`test_quote_stale`. There is no automatic retry on the next quote.

To continue testing, add the instrument row again with
`PUT /market-data/instances/INSTANCE_ID/instruments` and the setup body, then
call `POST /market-data/restart`.

## Waits and restarts

Resting orders and finite timers live only in the trading runtime's local
book. Any runtime restart loses that book. On the next reconciliation, old
open test orders are cancelled, with the remaining quantity released and no
invented fill. Finite waits do not resume. Previously applied fills and their
accounting remain; a partially applied split keeps its existing trades and
cancels only the remainder. This reconciliation also runs for a disabled
connection with open orders.

An infinite rest has no individual cancel operation: the ordinary manual
cancel refuses a connection-managed order with `venue_order_managed`.
New quotes and policy saves do not end it. Restarting the process, a connection
change that restarts trading, or a database restore loses the runtime book.
Stopping without starting again leaves the last stored open status visible
until reconciliation runs. The operator then sees `cancelled`, no new trades
for the lost remainder, and released reservations.

## Reading P&L

The engine remains authoritative for reservations, accounting and P&L. Test
execution supplies ordinary fills; the connector does not calculate balances
or P&L itself. Read `GET /balances?account=ACCOUNT_CODE&limit=1000` for
`available`, `held`, `incoming`, `averageEntryPrice`, `realizedPnl` and
`accountCurrency`. `GET /accounts/ACCOUNT_CODE` and MCP `list_accounts` expose
the ordinary account `pnl`. An opening buy changes the position and its entry
price while preserving realized P&L.

The price variant changes the fill price and therefore the engine's cost
basis and later realized result. With bid `99`, ask `101` and unchanged
quotes, the tested one-unit worst buy/sell round trip realizes `-2`. After that
round trip, `GET /balances?account=ACCOUNT_CODE&limit=1000` shows the BTC row
with `"realizedPnl": "-2.00"` and `"available": "0.00"`. In the same account,
the following one-unit best buy/sell round trip brings cumulative realized P&L
back to `0`. The BTC row is then no longer listed; only USD remains.
`GET /accounts/ACCOUNT_CODE` shows `"pnl": "0.00"`. Read the stored balances
and trades to inspect the result rather than treating an acknowledgement as
accounting.

These balance and account reads do not provide a separate unrealized-P&L
field. This guide specifies no unrealized-P&L read or local substitute for an
engine result.

## Forward testing only

This version executes against the currently applied quotes and local time.
It has no historical data, historical loader or replay. A finite delay is a
live wait, not a step through historical time.

## What historical backtesting would need

Historical backtesting would need an ordered replay of market events, orders
and timers through the same quote path; controlled time consistent with quote
freshness and execution waits; and reproducible engine and limit behavior.
This version defines no historical backtesting API or delivery date.
