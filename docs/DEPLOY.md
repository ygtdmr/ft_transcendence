# Deploying the backend stack

One command starts everything the backend needs:

```sh
make up      # generates secrets and a local certificate, builds, starts, waits until ready
make down    # stops, keeps data          make clean   # stops and DELETES data
make help    # every target
```

Requirements: Docker with Compose v2, `make`, `openssl`. The first build takes a few minutes.
The backend answers on `https://localhost:8443` (`INGRESS_PORT` in `.env` changes the port).

## What runs

| Container | Image | Role |
| --- | --- | --- |
| `ingress` | `nginxinc/nginx-unprivileged` | TLS, the public allowlist (`/api/` to the Web BFF, the two sockets, the avatar files), the cross-site write rule. The only published port, on `127.0.0.1`. |
| `web-bff` | built from `backend/web-bff` | The browser's REST entry (#88): origin check, session check through Auth, then the route table to Auth, User, Stats and Game Session. One public listener, no private one. |
| `auth-service`, `user-service`, `stats-service`, `game-session` | built from `backend/<service>` | One public listener (8080) and one private listener (8081, `/internal/v1`). |
| `game-gateway`, `notification-gateway` | built from `backend/<service>` | The room socket and the notification socket, plus a private listener. |
| `postgres` | `postgres:17.11-alpine` | One database and one login role per service. |
| `rabbitmq` | `rabbitmq:4.4.0-alpine` | The broker. It imports `deploy/rabbitmq/definitions.json` (queues, exchanges, bindings) each time it starts; that file is the topology of issue #189. |
| `auth-migrate`, `game-migrate` | `migrate/migrate` | One-shot: apply each service's `migrations/` before it starts. |

User Service and Stats apply their embedded migrations when they start (`MIGRATE_ON_START`). Redis is
left out: it is optional and never holds durable state.

The frontend is not served yet: it is not on an integration branch. When it lands, its static root and the
`try_files` fallback go into `deploy/ingress/nginx.conf`.

## Networks

```
 browser ──TLS──> ingress ──edge network──> web-bff, game-gateway, notification-gateway (:8080)
                                      └──> user-service (:8080, /static/avatars/ only)

 private network (internal: true, no route out)
   postgres, rabbitmq, web-bff ──> the public listeners (:8080) of Auth, User and Stats
   and every service's private listener (:8081), each on a fixed address

 game-rest network (internal: true, no route out)
   web-bff ──> game-session public listener (:8080), and nothing else is on it
```

- Only `ingress` publishes a port. The ingress is on `edge` only; PostgreSQL and RabbitMQ are on `private` only.
- `edge` holds the ingress, the Web BFF, both gateways, User Service (avatar files) and Auth (Google sign-in
  needs a route out). Stats and Game Session are not on it: their public listeners answer the Web BFF, and the
  ingress cannot reach them at all. The BFF reaches Stats as `stats-service.private:8080`.
- **Game Session's public listener is reachable from the Web BFF only.** Its room routes take the caller from the
  `X-User-Id` header and check no credential (decision on #304, recorded in #305): the header is trustworthy only if
  nobody but the BFF can send it. The gateways must reach Game Session's private listener, so they share `private`
  with it, and a listener bound to every interface would be open to them too. `PUBLIC_HOST` binds the public
  listener to Game Session's address on `game-rest`, a network that holds the BFF and Game Session and nothing else
  (the private listener binds `INTERNAL_HOST` on `private`, as everywhere). `make security-test` proves it: a
  container on `private` gets no connection to port 8080 by name or by address, and a container on `game-rest` does.
- Every service binds its private listener to its own `private` address (`INTERNAL_HOST`), so the ingress cannot
  connect to it even though it shares `edge` with four of them (`make security-test` checks all four).
- Auth trusts two proxies for the client address (`TRUSTED_PROXIES`): the ingress, which overwrites
  `X-Forwarded-For`, and the Web BFF, which appends the ingress's address. Both have fixed addresses.
- Services reach each other through `<name>.private:8081`. Those names exist on the private network only.
- **Plaintext on the private network is a decision, not an oversight.** Private calls are HTTP with a
  per-pair `Authorization: Bearer` credential. The network has no route out, no host can join it from outside, and
  nothing on it terminates TLS. This is the choice approved on #157 (11-24, security owner) with its conditions:
  an internal-only network, no host ports, a route allowlist at the ingress with `/internal` answered 404, a fresh
  random credential per pair, and capabilities dropped. If the stack ever spans hosts, the private listeners need
  TLS first.

## Secrets

`make secrets` (run by `make up`) creates what is missing under `secrets/` (git-ignored, mode 0700) and never
overwrites anything. Nothing secret is in an image, a Compose file or `VITE_*`.

| File | Used by |
| --- | --- |
| `credentials/<caller>-to-<receiver>` (15) | the caller's outbound credential and the receiver's inbound credential for that caller |
| `db/<service>.password` (4), `postgres_superuser` | PostgreSQL roles |
| `env/<service>.env` | `DATABASE_URL`, and for Stats `RABBITMQ_URL` |
| `jwt_secret` | Auth's signing key, mounted as `JWT_SECRET_FILE` |
| `google_client_secret` | Auth's Google client secret, mounted as `GOOGLE_CLIENT_SECRET_FILE` (empty: sign-in off) |
| `rabbitmq.conf` | the broker account |

The 15 pairs: `auth-service` to `user-service`, `game-session`, `game-gateway`, `notification-gateway`;
`user-service` to `auth-service`, `game-gateway`, `notification-gateway`, `stats-service`, `game-session`;
`game-session` to `user-service`; `stats-service`, `game-gateway`, `notification-gateway`, `web-bff` to
`auth-service`; `game-gateway` to `game-session`. A credential works for one receiver and the scopes that receiver
grants that caller; there is no shared secret. The Web BFF holds one credential, for Auth's `session.validate`.
Its `presence.read` call to User Service gets its pair when that route is added to the BFF.

Compose mounts these as plain bind mounts, so the files containers read are mode 0444 inside the 0700
`secrets/` directory, which is what keeps other users of the host out. This relaxes the "0400 files" condition
of the #157 decision: a container's unprivileged user cannot read a 0400 file owned by the host user on Linux,
and Compose ignores the `uid`/`mode` fields for file secrets.

### Rotating a credential

A receiver accepts a current and a previous credential per caller (`<CALLER>_PREVIOUS_CREDENTIAL_FILE`), so a
pair rotates without downtime:

1. Give the receiver the new value as current and the old one as previous; recreate the receiver.
2. Give the caller the new value; recreate the caller.
3. Remove the previous value from the receiver; recreate the receiver.

`make rotation-drill` runs steps 1 and 3 for one pair with temporary files and shows that both values work
during the overlap and the old one is refused afterwards. The real secrets are untouched.

## Verifying

`make integration-test` (issue #194) proves gateway and Auth admission and revocation through the real ingress with
a real WebSocket client: cookie-only admission (no query-string or header token, no `ft_refresh` as a session),
origin refused before the cookie is read, expired, wrongly signed and unknown sessions, membership and unknown
rooms, then logout and refresh-token reuse closing every tab with `4401` while other sessions and users keep
their sockets, nothing usable left for the revoked session, and a Game Gateway restart that members reconnect
after. Rooms are created and joined through the Web BFF with the users' own session cookies, as a browser does.
The same run proves the BFF's own admission: no, expired, forged and unknown sessions are a `401` on every protected
route, the caller is the session's user whatever identity headers the client sends, a cross-site write is a `403
FORBIDDEN_ORIGIN` before the session is looked at, and private paths are a `404`. It also stops Auth and User Service
for real: with Auth down a verified session is a `503 SERVICE_UNAVAILABLE`, never a pass-through and never a logout,
and the same session works again once Auth is back, with no trace of the refused room create.
It does not cover a second gateway instance, nor slow-client queue bounds (those are proven in each gateway's own
test suite). The run registers one account more than the gateway tests, five in all, which is Auth's registration
limit per client (five per ten minutes); `make integration-test` restarts Auth first to reset it.

`make security-test` runs 128 checks against the running stack: TLS versions and headers, cookie attributes,
CSRF/CORS/WebSocket origin, private listeners unreachable from the ingress, credentials and scopes, injection and
oversized input, rate limits, profile IDOR, container and database least privilege, and no secret in any log or
tracked file. It restarts Auth first because rate-limit counters live in memory.

## Release check

`make release-check` (issue #199) is the one command that produces the evidence a release proof needs. It starts
throwaway PostgreSQL 17.11, RabbitMQ 4.4.0 and two Redis 7 containers on random loopback ports (the stack's data is
never touched), and for each of the eight backend modules runs `gofmt -l`, `GOWORK=off go build`, `go vet`,
`go mod tidy -diff` and `go test -race -count=1 -v` with every real-dependency test enabled. It then brings the stack
up and runs `make security-test`, `make integration-test` and `make rotation-drill`.

It writes `release-evidence/<UTC time>-<commit>.md` (git-ignored) with the commit, the number of tracked files that
are modified, the host, Go, Docker and Compose versions, the exact commands, and per-module counts of passed, failed
and skipped tests, with the logs next to it. A failed step does not stop the run, but the exit status is non-zero if
any step failed **or any test was skipped**, because a skipped real-dependency test proves nothing
(`RELEASE_ALLOW_SKIPS=1` reports skips without failing). `scripts/release-check.sh` without `--stack` runs the modules
only.

It does not prove behaviour under representative load, a real Google sign-in, the frontend, a second gateway
instance or an upgrade with a new migration; the report says so.

## Real certificates

`make up` creates a self-signed certificate (`certs/`), which browsers warn about. For a real deployment put a CA
certificate at `certs/server.crt` and `certs/server.key` (the same file names) and set `PUBLIC_BASE_URL` in `.env`
to the public `https://` origin: the gateways refuse any other `Origin`, and Auth builds the Google redirect URI
from it. Google sign-in is off until `GOOGLE_CLIENT_ID` is set in `.env` and the client secret is written to
`secrets/google_client_secret`; Auth refuses to start with only one of the two.

## Known gaps (stated, not hidden)

| Gap | Effect | Owner |
| --- | --- | --- |
| RabbitMQ topology and event names (#189, #171, #141) | `deploy/rabbitmq/definitions.json` holds only the queue Stats consumes (`stats.game-events`), so Stats' consumer connects, but nothing publishes or binds to it: no event flows, and room events never reach the room socket. The file is a placeholder for the real topology: replace it, nothing else in the stack changes. Importing only adds: changing the arguments of a queue that already exists needs `make clean` or deleting that queue. The file must not contain users (the account is generated), and the broker's `load_definitions` setting must not replace the import: on a fresh volume it skips creating that account. | zehraxaa |
| Game Session trusts `X-User-Id` on its public listener, with no service credential (decision on #304, option A) | Isolation holds the line: only the Web BFF can connect (`PUBLIC_HOST` on `game-rest`, see Networks), and `make security-test` checks it. What it does not cover is the Web BFF itself: a compromised BFF could act as any user in the room routes. If that is not acceptable later, the BFF sends a credential and Game Session checks it with its existing `RequireScope`. `PUBLIC_HOST` is a Game Session change that the stack depends on. | ygtdmr (BFF), BercemES (Game Session) |
| The Web BFF does not serve `GET /api/v2/rooms/{room_id}/presence` yet | The route is answered 404 until the BFF composes Game Session members with User Service presence; its `web-bff` to `user-service` pair is added then. | ygtdmr |
| REST services have not adopted the shared origin guard (#2) | The ingress enforces the contract's cross-site rule; the services behind it do not. | service owners |
| `DATABASE_URL` and `RABBITMQ_URL` come from the environment (#157 F4) | Visible to anyone who can run `docker inspect` on the host. Auth's signing key and Google secret are files since #303; User, Stats and Game Session have no `_FILE` form for the database URL yet. | service owners |
| One instance of each service | The Game Gateway contract assumes a single instance. | contract |

## Repository access (issue #191, handover item)

Checked 2026-11-27 with `gh api repos/ygtdmr/ft_transcendence/collaborators`: four collaborators (`ygtdmr` admin;
`guryelf`, `zehraxaa`, `BercemES` write). The former members UmutSogukpinar and Melovii are not listed. Deploy
keys, webhooks and Actions secrets need admin rights to read and were not checked: the repository admin has to
confirm that none was created by a former member, and that any credential they could see has been rotated. This
document changes no permission.
