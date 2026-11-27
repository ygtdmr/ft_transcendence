# Backend service conventions and skeleton

Every backend service is its own Go module and its own container, built on
`backend/shared`. A new service starts from the skeleton in `skeleton/`. The
skeleton is compiled and tested with this module (`tests/unit/skeleton_test.go`),
so it always matches the shared library, but no service imports it: copy it.

The first service built this way is `notification-gateway` (#276).

## Start a new service

1. Copy `skeleton/` to `backend/<service>/internal/app/` and
   `skeleton/cmd/server/main.go` to `backend/<service>/cmd/server/main.go`.
   Then:
   - change the package import path;
   - set `ServiceName` to the directory name;
   - replace the example caller and route with your rows of the private
     authorization matrix.
2. Create `backend/<service>/go.mod`:

   ```
   module github.com/melmut-42/ft_transcendence/backend/<service>

   go 1.27.1

   require github.com/melmut-42/ft_transcendence/backend/shared v0.0.0

   replace github.com/melmut-42/ft_transcendence/backend/shared => ../shared
   ```

   Then run `GOWORK=off go mod tidy`.
3. Add `./<service>` to `backend/go.work`. Two tests fail otherwise:
   - `TestWorkspaceListsEveryBackendModule` checks that every module is listed;
   - `TestBackendModulesShareOneGoVersion` checks that every module uses the
     workspace's Go version (one toolchain pin, see #133).
4. Copy `skeleton/Dockerfile`. Copy the service's own directory next to `shared`
   and build `./cmd/server` from it, as `notification-gateway/Dockerfile` does.
5. Add `.env.example` with placeholders only, and a README.

## Layout

```
backend/<service>/
  cmd/server/main.go        signal context, then app.Main; nothing else
  internal/config/          Load(lookup) through config.Reader (or keep it in app)
  internal/app/             assembly: listeners, routes, lifecycle
  internal/...              the service's own packages
  migrations/               this service's migrations only
  tests/unit/, tests/integration/
  Dockerfile, .env.example, README.md, go.mod, go.sum
```

## Configuration

All settings come from the environment through `config.Reader`. It collects
every problem and reports them together, by variable name. An error never
contains a value, a file path or a secret, and an invalid configuration stops
startup before any listener opens.

| Setting | Name | Notes |
| --- | --- | --- |
| Environment | `APP_ENV` | `development` (default), `production`, `test` |
| Logs | `LOG_FORMAT`, `LOG_LEVEL` | Defaults follow `APP_ENV` (production: JSON at info; test: text at error) |
| Public listener | `PORT` | Default 8080 |
| Private listener | `INTERNAL_PORT`, `INTERNAL_HOST` | Default 8081 and every interface. `config.Reader.InternalAddress` reads both. A deployment sets `INTERNAL_HOST` to the container's private-network address, so the private listener is not reachable from the reverse proxy's network. |
| Own database | `DATABASE_URL`, `MIGRATE_ON_START` | Only the service's own database |
| Calling another service | `<RECEIVER>_INTERNAL_URL`, `<RECEIVER>_INTERNAL_TIMEOUT`, `<RECEIVER>_OUTBOUND_CREDENTIAL_FILE` | Through `privateclient.ConfigFromEnv`. URL: `AUTH`, `USER`, `GAME`, `STATS`. Credential: `AUTH_SERVICE`, `USER_SERVICE`, `GAME_SERVICE`, `STATS_SERVICE`, `GAME_GATEWAY`, `NOTIFICATION_GATEWAY`. Several instances: `<GATEWAY>_INTERNAL_URLS`. |
| Accepting a caller | `<CALLER>_CREDENTIAL_FILE`, `<CALLER>_PREVIOUS_CREDENTIAL_FILE` | Through `serviceauth.LoadCredentials`. The previous file is set only during a rotation. An unconfigured caller is refused (fail closed) and reported at startup. |
| Broker | `RABBITMQ_URL` | Through `rabbitmq.ConfigFromEnv` |
| Public origin | `PUBLIC_BASE_URL` | The external origin, no path |
| Shutdown | `SHUTDOWN_TIMEOUT`, `SHUTDOWN_DRAIN_DELAY`, `SHUTDOWN_CLOSE_TIMEOUT` | Through `lifecycle.OptionsFromEnv` |

**Secrets are files.** A credential is a mounted file of at least 32 bytes
(`openssl rand -hex 32`, mode 0400), named by a `*_FILE` variable. It is never a
plain environment value, never in an image, never in `VITE_*` and never logged.

## Listeners and routes

- **Public listener (`PORT`):** `/healthz` and `/readyz`, plus the service's
  `/api/...` or `/ws/...` routes. It never serves `/internal/*`.
- **Private listener (`INTERNAL_HOST:INTERNAL_PORT`):** `/internal/v1/*` only,
  each route behind `serviceauth.Require(scope)`.
- **Both use `NewEngine`:**
  - request ids and trace context (`ginhttp.Trace`);
  - access logs without credentials;
  - panic recovery;
  - the `{"error": {...}}` envelope, including `NOT_FOUND` and `METHOD_NOT_ALLOWED`.
- **Both use `NewServer`'s timeouts.** A handler that hijacks connections
  (WebSockets) closes them itself at shutdown, through `srv.RegisterOnShutdown`
  or a lifecycle component.

## Lifecycle and logs

- **Health checks.** `lifecycle.Health` answers `/healthz` (process only) and
  `/readyz`, which checks the dependencies registered with `health.Add`.
- **Shutdown.** `lifecycle.Serve` runs each listener and shuts it down in a
  fixed order:
  1. readiness fails;
  2. in-flight requests finish within `SHUTDOWN_TIMEOUT`;
  3. components close in reverse order.
- **Logs.** `logging.New(cfg.Logging, os.Stdout)` writes structured logs with
  `service`, `environment` and `request_id`, and redacts credential-like keys.

## Builds and dependencies

- **Standalone builds.** Every module builds on its own, the way its Dockerfile
  builds it: `GOWORK=off go build ./...`.
- **Shared dependencies.** `backend/shared` is one module for every service.
  Never raise one of its dependencies above what a service pins. After any
  shared change, run this in every service:
  `GOWORK=off go build ./... && GOWORK=off go mod tidy -diff`.
- **Images.** Distroless, non-root; `CGO_ENABLED=0 GOWORK=off go build -trimpath`.

## Migrations

- **Own directory.** Each service keeps its migrations in its own
  `migrations/` directory, with its own numbering, against its own database.
- **No sharing.** Sequences are never shared, and no service reads or migrates
  another service's schema.

## Current state (2026-11-25)

The setting names above are the ones Auth, User and Stats already use. Each of
them still has its own loader, not `config.Reader`; moving onto the shared one
is each owner's change. Known differences, left to their owners:

| Service | Difference | Convention |
| --- | --- | --- |
| game-session | `USER_SERVICE_INTERNAL_URL`, `GAME_TO_USER_SERVICE_CREDENTIAL_FILE` | `USER_INTERNAL_URL`, `USER_SERVICE_OUTBOUND_CREDENTIAL_FILE` |
| game-session | No `*_PREVIOUS_CREDENTIAL_FILE`; no `<RECEIVER>_INTERNAL_TIMEOUT` | Rotation with two credentials (finding F2 on #157); a configured timeout |
| game-session | Does not read `APP_ENV`, `LOG_FORMAT`, `LOG_LEVEL` | Common settings |
