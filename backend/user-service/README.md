# user-service

Owns canonical profiles, avatars, the social graph, reports, presence queries and
account deletion. It does not store passwords, tokens, game state or statistics.
The API contract is the Bruno collection on the `bruno` branch.

This is the bootstrap: configuration, a schema-scoped PostgreSQL connection,
embedded migrations, request IDs, the common error envelope and health checks.

## Endpoints

| Method and path  | Meaning                                                                            |
| ---------------- | ---------------------------------------------------------------------------------- |
| `GET /healthz` | Liveness. 200 while the process serves HTTP. Never touches the database.           |
| `GET /readyz`  | Readiness. 200 while PostgreSQL answers, otherwise 503 `DEPENDENCY_UNAVAILABLE`. |

Both sit outside `/api/v1` so the public reverse proxy does not expose them.
Success bodies use `{"data": ...}`, errors use `{"error": {"code", "message", "details"}}`.

## Configuration

Copy `.env.example` to `.env` for reference. The service reads the process
environment only and does not load `.env` itself.

| Variable                      | Default         | Meaning                                                                     |
| ----------------------------- | --------------- | --------------------------------------------------------------------------- |
| `DATABASE_URL`              | none, required  | `postgres://` URL. The service creates and uses its own `users` schema. |
| `PORT`                      | `8080`        | Public HTTP port (health routes).                                           |
| `INTERNAL_PORT`             | `8090`        | Private listener for `/internal/v1/*`. Must differ from `PORT`.             |
| `APP_ENV`                   | `development` | `development`, `production` or `test`. Sets log defaults.             |
| `LOG_FORMAT`, `LOG_LEVEL` | by `APP_ENV`  | `text` or `json`; `debug`, `info`, `warn` or `error`.           |
| `MIGRATE_ON_START`          | `true`        | Apply embedded migrations at startup.                                       |
| `AVATAR_BASE_URL`           | none            | Public `https://` origin for avatar URLs. Empty gives root-relative URLs. |
| `AVATAR_STORAGE_DIR`        | none            | Absolute directory for uploaded avatars. Required in production; when empty, avatar changes are not served. |
| `REPORT_WINDOW`             | `24h`           | How long a reporter waits before reporting the same user again, as a Go duration. |
| `AUTH_SERVICE_CREDENTIAL_FILE` | none, optional | File holding Auth Service's service credential (32+ characters).      |
| `AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE` | none, optional | Previous Auth credential, accepted during a rotation.       |
| `CHANNEL_SERVICE_CREDENTIAL_FILE` | none, optional | File holding Channel Service's service credential (32+ characters). |
| `CHANNEL_SERVICE_PREVIOUS_CREDENTIAL_FILE` | none, optional | Previous Channel Service credential, accepted during a rotation. |
| `GAME_SESSION_CREDENTIAL_FILE` | none, optional | File holding Game Session's service credential (32+ characters). |
| `GAME_SESSION_PREVIOUS_CREDENTIAL_FILE` | none, optional | Previous Game Session credential, accepted during a rotation. |
| `WEB_BFF_CREDENTIAL_FILE` | none, optional | File holding Web BFF's service credential (32+ characters). |
| `WEB_BFF_PREVIOUS_CREDENTIAL_FILE` | none, optional | Previous Web BFF credential, accepted during a rotation. |
| `GAME_GATEWAY_INTERNAL_URLS`, `CHAT_GATEWAY_INTERNAL_URLS` | none | Comma-separated private origins of every running Gateway instance. Required in production. |
| `GAME_GATEWAY_OUTBOUND_CREDENTIAL_FILE`, `CHAT_GATEWAY_OUTBOUND_CREDENTIAL_FILE` | none | This service's credential for each Gateway kind. Set with the URLs. |
| `GATEWAY_INTERNAL_TIMEOUT` | `5s` | Upper bound for one registry query, as a Go duration. |
| `STATS_INTERNAL_URL`, `STATS_SERVICE_OUTBOUND_CREDENTIAL_FILE` | none | Stats' private listener and this service's credential for it. Required in production; without them the public profile route is not served. |
| `STATS_INTERNAL_TIMEOUT` | `5s` | Upper bound for one call to Stats, as a Go duration. |

## Internal API

Private routes serve other services, never browsers. They are served only on
the private listener (`INTERNAL_PORT`), which belongs on the private service
network; the public listener does not serve `/internal/*`. The contract is the
`Internal REST v1 - Services` Bruno collection on the `bruno` branch.

Every request carries `Authorization: Bearer <service credential>`. Each caller
has its own credential, read from a mounted secret file, and a fixed set of
scopes on this service. A missing, malformed or unknown credential gets
`401 SERVICE_UNAUTHENTICATED`; a known caller without the scope gets
`403 SERVICE_FORBIDDEN`. Without configured credentials every private request
is rejected. To rotate a credential, set the new file as current and the old
one as previous, switch the caller, then remove the previous file.

| Route | Caller and scope | Purpose |
| --- | --- | --- |
| `PUT /internal/v1/provisioning-operations/{operation_id}` | `auth-service`, `profile.provision` | Create the profile once per operation |
| `GET /internal/v1/provisioning-operations/{operation_id}` | `auth-service`, `provisioning.read` | Read an operation's receipt |
| `GET /internal/v1/provisioning-operations?user_id=` | `auth-service`, `provisioning.read` | Read the receipt of a user's operation |
| `GET /internal/v1/profiles/{user_id}` | `auth-service`, `profile.read` | Read the current canonical profile |
| `GET /internal/v1/friendship-verdicts?actor_user_id=&target_user_id=` | `channel-service`, `game-session`, `friend.verdict` | Read the active-friend verdict |
| `GET /internal/v1/presence?user_id=` | `web-bff`, `presence.read` | Read presence for 1 to 100 users |

Provisioning takes `user_id`, `username` and `created_at` in the body. It
answers `201` with a new receipt or `200` with the stored receipt for an
identical retry. The receipt (`operation_id`, `user_id`, `outcome`,
`completed_at`) comes from the operation record and never changes. Conflicts are
decided in this order: `409 USER_DELETED`, `409 OPERATION_CONFLICT`,
`409 USER_ALREADY_PROVISIONED`, `409 USERNAME_TAKEN`. A database outage answers
`503 SERVICE_UNAVAILABLE`. Requests for one user are serialized with a
transaction-scoped advisory lock; account deletion must take the same lock.

The canonical profile read returns `user_id`, `username`, `avatar_url` and the
provisioned `created_at` from the current row, never the login email,
credentials, presence or relationships. A user without a profile is
`404 USER_NOT_FOUND`; a recorded deletion is `409 USER_DELETED`. A default
avatar is `/static/avatars/default/{user_id}.svg`; presets use
`/avatars/presets/{preset_id}.png` and uploads `/static/avatars/uploaded/{key}`.

The active-friend verdict is `{ "data": { "active_friendship": bool } }`, read
from current records on every call. It is `true` only when the friendship is
active, neither user blocks the other and neither is being deleted. `false` also
covers an unknown user and never says which condition failed, so it cannot reveal
a block or its direction. A delayed event never replaces this answer. A database
outage is `503 SERVICE_UNAVAILABLE`; callers must never treat a failure as `false`.

`internal/client/authclient` calls Auth's `GET /internal/v1/accounts/{user_id}`
with this service's own credential. Auth outages, timeouts and malformed
answers are reported as unavailable, never as a missing account; a rejected
credential is a configuration fault.

## Run locally

With Docker, from this directory:

```sh
mkdir -p .secrets && openssl rand -hex 32 > .secrets/auth_to_user_credential
docker compose up --build
curl localhost:8081/readyz
```

Without Docker for the service itself, from this directory, with a PostgreSQL
reachable at `DATABASE_URL`:

```sh
export DATABASE_URL='postgres://user:change-me-local-only@localhost:5433/user?sslmode=disable'
export PORT=8081
go run ./cmd/server
```

## Migrations

SQL files live in `migrations/` as `NNNNNN_description.up.sql` and `.down.sql`.
They are embedded in the binary and applied in order at startup. Progress is
recorded in `users.schema_migrations`. Several instances starting together are safe.

## Database schema

All tables live in the `users` schema. Cross-service ids (`user_id`, `room_id`)
are plain numbers with no foreign key. Foreign keys exist only inside `users`.

| Table                                                | Holds                                                                                                |
| ---------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| `profiles`                                         | One row per Auth user. Username is unique ignoring case and surrounding spaces.                      |
| `friendships`                                      | One row per unordered pair, `user_low < user_high`. `active = false` means suspended by a block. |
| `friend_requests`                                  | Requests with history. One `pending` request per unordered pair.                                   |
| `blocks`                                           | Directional: blocker to blocked. Blocking suspends the friendship and cancels pending requests.      |
| `reports`                                          | Moderation reports. `users.file_report()` enforces one report per reporter and target per window.  |
| `provisioning_operations`, `deletion_operations` | Idempotent, resumable workflow records keyed by `operation_id`.                                    |
| `outbox`                                           | Transactional outbox with a stable `event_id` per event. Unpublished rows have `published_at IS NULL`. |

## Public routes and sessions

Friends are served under `/api/v1/friends`, friend requests under
`/api/v1/friends/requests`, username search at `/api/v1/users/search`, and blocks
under `/api/v1/users/{user_id}/block`, and public profiles at `GET /api/v1/users/{user_id}`. Avatars are served under
`/api/v1/avatars/presets` and `/api/v1/users/me/avatar`. The `ft_session` cookie is the only browser credential: each request forwards it to Auth's private
session validation. A refused session is `401 UNAUTHORIZED`; an Auth outage is
`503 SERVICE_UNAVAILABLE`. Set `AUTH_INTERNAL_URL` and
`AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE` to enable these routes; both are required
in production.

A public profile combines the canonical profile and the viewer's relationship
with Stats counters and presence. A missing user or one being deleted is
`404 USER_NOT_FOUND`. A Stats or presence failure is `503 SERVICE_UNAVAILABLE`,
never zero counters or offline. Penalty points are not part of it. The own
profile at `GET /api/v1/users/me` is not served yet.

## Presence

Presence comes from the active socket registries of every configured Game
Gateway and Chat Gateway instance, queried concurrently through
`GET /internal/v1/connections` on each read; the service stores no presence
state. A user is online when any instance reports them and offline when every
instance answers without reporting them. Otherwise presence is unknown, and the
read returns `503 SERVICE_UNAVAILABLE` instead of `is_online: false`.

Accept friend request reads the sender's presence before committing, so an
unknown presence leaves the request pending and the call can be retried.

Without Gateway settings, which only non-production environments allow, every
user is reported offline and startup logs a warning.

Every friend request and block change takes the pair lock first, then the
request row, and records its integration events in the same transaction.

A block cancels the pair's pending request and suspends their friendship.
Unblocking lifts only the caller's own block. The friendship becomes active again
once neither user blocks the other; a cancelled request stays cancelled.

Username search matches a case-insensitive substring and treats the query as plain
text. It omits the caller and users being deleted. Each result shows the caller's
relationship to that user; a block placed by the other user reads as `NONE`. The
search has no substring index, so it scans `profiles`.

## Reports

`POST /api/v1/users/{user_id}/reports` stores a report for moderation. The reporter
comes from the session, the reported user is not notified, and no route reads
reports back. One reporter can report one user once per `REPORT_WINDOW`; a repeat
is `409 ALREADY_REPORTED` with `retry_after_seconds`. No other throttling is applied.

## Avatars

An upload is identified by its bytes only: JPEG, PNG or WebP, at most 2 MiB, 32 to
4096 pixels per side and 16 megapixels. The service stores a new 256x256 PNG
cropped to the center square, so metadata and hidden data are dropped. Client
filenames and types are never used.

Each upload gets a random key, and the file is written before the profile changes.
The replaced file is removed after the commit. A crash between those steps leaves
an unused file behind; nothing removes it yet.

`GET /static/avatars/uploaded/{key}` and `GET /static/avatars/default/{user_id}.svg`
serve the images without a session. Preset images are served by the frontend at
`/avatars/presets/{preset_id}.png`.

## Integration events

A change that other services must learn about records its event with
`postgres.AppendEvent` inside the change's own transaction, so the change and
the event commit or roll back together. Event types come from the catalog in
`internal/service/outbox.go` (`user.profile.updated`, `friend.request.received`,
`friend.request.resolved`, `friend.removed`, `friend.restored`,
`relationship.blocked`); other types are refused.

Block changes record these payloads:

| Event                     | Payload                                                 | Consumers                   |
| ------------------------- | ------------------------------------------------------- | --------------------------- |
| `friend.request.resolved` | `request_id`, `from_user_id`, `to_user_id`, `status`    | Chat Gateway                |
| `friend.removed`          | `user_low`, `user_high`                                 | Chat Gateway                |
| `friend.restored`         | `users`: both users, ordered by `user_id`               | Chat Gateway                |
| `relationship.blocked`    | `blocker_id`, `blocked_id`                              | Channel Service only        |

`relationship.blocked` is recorded only when a block suspends a friendship, before
that friendship's `friend.removed`. It names which user placed the block so
Channel Service can choose each user's access reason. It must never be delivered
to a user socket.

Removing a friend records `friend.removed` with the same payload, in the same
transaction that deletes the friendship.

An avatar change records `user.profile.updated` with `user_id`, `username`,
`avatar_url` and `recipient_user_ids`: the user, their active friends and the other
side of their pending requests.

`service.OutboxRelay` publishes recorded events at least once:

- Each event has a stable `event_id`. A redelivered event carries the same
  `event_id`, so consumers deduplicate by it.
- A row is marked published only after the publisher reports a broker confirm,
  in the same transaction that claimed it. A crash after the confirm sends the
  event again; it is never lost.
- Events of one aggregate (for example one profile) publish in `id` order. A
  failed event records `attempts`, a short code in `last_error` and a later
  `next_attempt_at` (1s doubling to 5 minutes), and holds back later events of
  its aggregate only.
- Several relays may run at once; `FOR UPDATE SKIP LOCKED` keeps them apart.

The published envelope is `{ "event_id", "event_type", "occurred_at", "payload" }`.
The RabbitMQ publisher and its exchange and routing are not implemented yet, so
the relay is not started by the server.

Accepting a request: in one transaction, set the request to `accepted`, then
insert the friendship. The database refuses a new pending request while an
active friendship exists, and it takes a per-pair lock so this holds under races.

## Tests

From this directory:

```sh
go test ./tests/unit
go test ./tests/integration
```

Integration tests need a PostgreSQL server where the role may create databases.
They create and drop a throwaway database per test, and are skipped when the
variable is unset:

```sh
USER_TEST_DATABASE_URL='postgres://user:change-me-local-only@localhost:5433/user?sslmode=disable' \
  go test ./tests/integration
```

## Layout

```
cmd/server             entry point and wiring
internal/config        environment settings
internal/controller    HTTP handlers
internal/middleware    request IDs
internal/router        error-returning handler adapter
internal/server        Gin engine and middleware chain
internal/service       rules, no HTTP or SQL
internal/repository    PostgreSQL access, migrations runner
internal/repository/avatarfiles   avatar image files
migrations             embedded SQL
tests/unit             no database needed
tests/integration      real PostgreSQL
```
