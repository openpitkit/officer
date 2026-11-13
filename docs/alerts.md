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
A renderer provides a summary and detail component with its actual decision
controls and passes validated form values to the supplied `resolve` operation.
It uses the server's action list. Unknown renderers show an explicit error;
there is no generic JSON editor or invented business decision.

The default composition registers no concrete business types or synthetic events.

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

Browser OS notifications and PWA badges are outside this slice.
