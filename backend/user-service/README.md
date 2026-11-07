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
| `AUTH_SERVICE_CREDENTIAL_FILE` | none, optional | File holding Auth Service's service credential (32+ characters).      |
| `AUTH_SERVICE_PREVIOUS_CREDENTIAL_FILE` | none, optional | Previous Auth credential, accepted during a rotation.       |

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
| `outbox`                                           | Transactional outbox. Unpublished rows have `published_at IS NULL`.                                |

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
migrations             embedded SQL
tests/unit             no database needed
tests/integration      real PostgreSQL
```
