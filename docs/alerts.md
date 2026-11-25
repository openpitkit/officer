# Actionable alerts

Actionable alerts are durable business events that stay open until a registered
decision succeeds. Reading an alert never resolves it. The standalone panel
shows the launcher above the sidebar service entries on every working screen,
including when there are no open events. Its main count is unread alerts; the
second line counts all unresolved events. The mobile navigation button carries
the unread count.

## Personal state and decisions

Opening the drawer changes no read marks. Expanding an event marks that event
read; its detail also offers mark read and mark unread. Marks live on the server
and use only the principal resolved by the authentication layer. Officer's local
composition identifies admitted sessions as the same operator, so separate
browser sessions see the same marks. Hosts with distinct trusted principals
keep their marks separate. There is no dismiss or delete operation for an open
event.

A successful decision returns its event, action, result, actor, source and time.
Its audit entry retains the alert ID, action, result and caller attribution;
writing that entry and removing the open event and its personal marks happen in
one SQLite transaction. No separate closed-alert history is retained. A
validation error, failed decision or failed final
transaction leaves the event open. Domain handlers must provide idempotency for
their own external effects; this mechanism does not promise exactly-once
external execution after a crash or a failed final transaction.

## Composition

`framework/alerts` owns the independent service and its narrow store capability.
It does not extend the general Node, ControlPlane, MCP Source or Store interfaces.
The SQLite realm handle implements the alert capability. Officer registers the
same service through additive HTTP and MCP registrars.

Hosts register business types through `Config.AlertTypes`. Each `alerts.Type`
names its payload validator and supported `alerts.Action` entries. Every action
has an input validator and a resolution handler returning a JSON result. Unknown
types, unknown actions, malformed payloads and invalid action inputs are refused.
`Config.AlertServiceReady` connects event producers to the constructed service.
Producers call `Service.Publish` with a stable event ID, type, timestamp, source
and payload and an explicitly stamped trusted caller context.

A type can supply `Visible` to restrict reads and decisions under the caller's
account scope. Hidden events contribute neither totals nor unread counts; detail,
read marks and decisions return not-found without exposing payload, actions or
delivery metadata. Types without that callback remain realm-wide. Publication and
trusted configured channel delivery are independent of reader visibility.

Register additional enabled delivery channels through `Config.AlertChannels`.
Officer installs the panel channel automatically. Publication stores the event
before attempting delivery. The panel channel writes a durable inbox receipt;
the polling panel reads all stored open events even when a delivery channel
fails. Every channel is attempted independently. Delivery outcomes are stored
with the event and audited. The drawer shows failed channels without exposing
raw transport errors. If publication reports a delivery error, the event already
exists; a producer must retain its stable ID instead of creating another event.
A channel may finish after the operator has successfully resolved the event.
When the existing resolution audit proves that lifecycle state, the late outcome
is audited without recreating the event or its receipts. An unknown event ID is
refused.
Publication refuses a previously resolved ID when its resolution audit remains
available.

The web composition calls `registerAlerts` from the standalone `main.tsx`.
Shared `registerOfficerDefaults` does not install alerts for Cloud consumers.
Concrete panel types call `registerAlertRenderer` from `web/src/alerts/registry`.
A renderer provides a summary text function that takes the alert and translation
function `t`. Its detail component renders the decision controls and passes
validated form values to the supplied `resolve` operation.
It uses the server's action list. Unknown renderers show an explicit error;
there is no generic JSON editor or invented business decision.

The standalone composition registers `mixed_trading_access`. Its payload contains
only the Officer account code and title. A successful grant, account creation,
group move or restore publishes it on a transition from non-mixed access to
effective paper and live access. Effective access includes inherited grants and
disabled connections. Reads, no-op writes and changes to an already mixed account
do not publish; a later transition into mixed access publishes again. Failed
mutations and internal restore rollback do not publish.

The only action is `acknowledge`, taking an empty object. It records awareness
through the normal attributed resolution and changes no grants, connections or
orders. The persistent current-access warning remains until access is no longer
mixed, independently of alert acknowledgement. Custom node hosts may implement
`node.TradingAccessObserverHost` to attach the producer; shared web defaults retain
host-owned alerts composition.

The mutation boundary serializes before/after inspection and captures transition
identities. Synchronous publication and delivery run after all administrative
locks and reservations are released. Restore finishes trading restart, signer
reload and manual quote publication first. Cancellation after commit or a failed
access audit does not skip the warning. A
publication failure reports `trading_access_notification_failed` after the access
has been committed. All newly mixed accounts in a group change or restore are
attempted even when one publication fails. There is no outbox or automatic mutation
replay. The event ID is created once per publication; delivery failure preserves
the stored event and its failed-channel receipts.
Independent operational failures joined with warning failures report
`trading_access_completion_failed`, rather than warning-only guidance. The restore
summary is retained when restore commits and warning delivery fails.

The mixed-access type uses the installed control plane's exact scoped account
read, including when a host replaces the backend factory. Hidden accounts remain
hidden to standalone token-mode MCP agents in single-account mode. A missing or
failed visibility binding fails closed. Visible accounts and multi-account mode
retain normal warning reads and acknowledgement.

The standalone composition also registers `order_awaiting_review`. Submit,
send-paper and send-live publish it after committing an order that enters
`awaiting_review`. Its event ID is the order's external ID. The payload contains
only identifiers: `order` (that external ID), `account` (the account code) and
`markCode` (the first review mark's code).

After an accepted review confirmation, force or review cancel, the producer
resolves the event through `close` with the deciding caller's attribution, after
the operation and any venue send complete. A confirmation that rejects leaves
the order waiting and the alert open. The only action is `close`, taking an
empty object. It succeeds only when the order no longer waits for review or no
longer exists; while the order waits, it returns 409 `conflict` and keeps the
alert open.

An alert failure after the order decision commits does not undo the decision or
a venue send. The caller receives the optional `alertFailure` member with
`step` (`publish` or `close`) and `message`, keeping the operation's status code.
`publish` also covers delivery failure after storage; the alert then exists and
is listed.
Closing when no open alert exists is a failure, not a no-op. Custom node hosts
may implement `node.OrderReviewObserverHost` to attach this producer, as with
`node.TradingAccessObserverHost`; hosts without it receive no review alerts.

Visibility follows the order's account through the same scoped account read as
`mixed_trading_access`. An alert whose account was deleted is hidden from every
reader and cannot be closed until an account with the same code exists again.
Accepted gaps, with no reconciliation, are a crash between an order's commit
and its alert publication or close, a restore that splits orders from alerts, an
account or instrument cascade delete, and a race where a review decision commits
before alert creation (the decision reports `alertFailure` with step `close`,
the late alert stays open, and the operator's Close succeeds).

Normal all-section backups preserve open events, every principal's read marks
and delivery outcomes. These live under `activity_history`, like orders and
their approvals, and are realm-wide even when an account selector is present.
A backup that explicitly excludes that section excludes this state as well.
Restore supports replace-all, overwrite and insert-missing with the existing
section semantics. Resolution history follows the separately selected audit log.

## Authenticated surfaces

The existing authentication and authorization layers protect all alert routes.
The panel uses their `/app/api/v1` mirror; REST uses `/api/v1`.

| Method | Relative route | Operation |
| --- | --- | --- |
| GET | `/alerts` | List open events, personal marks and unread/total counts |
| GET | `/alerts/{id}` | Read one event without changing its mark |
| PUT | `/alerts/{id}/read` | Set the required boolean `read` member |
| POST | `/alerts/{id}/actions/{action}` | Execute a registered action with the required `input` member |

The guarded MCP tools are `list_alerts`, `get_alert`, `set_alert_read` and
`resolve_alert`. Mutating tools follow the existing command-enable policy and
start disabled. Neither surface accepts a caller principal parameter.

## Operator verification

In a test composition, register a paper-data type, renderer and producer through
the same APIs used by real business types. Publish an event with successful and
refusing decisions and enable a failing secondary channel. Open any working
screen, open the sidebar drawer and expand only that event. Verify the unread
count changes while the unresolved count stays fixed and the delivery warning
remains visible. Open a second authenticated operator session and verify mark
read/unread is shared. A refused decision must display an error and retain the
event; a successful one must remove it and leave attributed result/audit data.
Escape closes the drawer and returns focus to its launcher. Repeat on mobile
using the existing navigation affordance.

## Browser notifications

Browser notifications run while a standalone panel tab is open. Delivery uses
the browser's Notifications API without Web Push or a service worker.
Browsers throttle timers in background tabs, so notifications there can take
up to about a minute to arrive even when the poll interval is shorter.

The drawer shows whether notifications are unavailable, off, on, or blocked.
They are unavailable if the browser lacks the Notifications API or cannot
create notifications from a page, as in Android Chrome.
**Allow notifications** stays available while permission is undecided. If
blocked, allow them in the browser's site settings and reload. A separate
notice reports browser errors, refusals to display a notification, or failed
permission requests. It clears when a new permission request starts.
Permission is per origin. The panel stores no notification settings.

The first successful alerts load establishes the baseline. Each successful
load adds all its alert IDs to an in-memory set for that tab. Later loads
notify only for unread IDs that are new to the set, and only with permission.
Each open panel tab notifies on its own, so with several tabs open the same
alert can raise one notification per tab (browsers that replace notifications
with the same tag may show one). Clicking a notification
focuses the tab and opens the drawer. If the alert is still in the list, it
expands and uses the standard mark-read action.

The tab title shows the unread count and the favicon shows an unread dot
regardless of notification permission. Both return to their original values
when the unread count is zero or unknown. Read marks are held on the server.
**Mark unread** changes the count without sending a new notification. The
panel never sees an alert opened and resolved between two polls.
