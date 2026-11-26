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
| `ingress` | `nginxinc/nginx-unprivileged` | TLS, the public route allowlist, the cross-site write rule. The only published port, on `127.0.0.1`. |
| `auth-service`, `user-service`, `stats-service`, `game-session` | built from `backend/<service>` | One public listener (8080) and one private listener (8081, `/internal/v1`). |
| `game-gateway`, `notification-gateway` | built from `backend/<service>` | The room socket and the notification socket, plus a private listener. |
| `postgres` | `postgres:17.11-alpine` | One database and one login role per service. |
| `rabbitmq` | `rabbitmq:4.4.0-alpine` | The broker. Its topology is issue #189. |
| `auth-migrate`, `game-migrate` | `migrate/migrate` | One-shot: apply each service's `migrations/` before it starts. |

User Service and Stats apply their embedded migrations when they start (`MIGRATE_ON_START`). Redis is
left out: it is optional and never holds durable state.

The frontend is not served yet: it is not on an integration branch. When it lands, its static root and the
`try_files` fallback go into `deploy/ingress/nginx.conf`, as in the `devops` branch's gateway.

## Networks

```
 browser ──TLS──> ingress ──edge network──> public listeners (:8080)

 private network (internal: true, no route out)
   postgres, rabbitmq, and every service's private listener (:8081), each on a fixed address
```

- Only `ingress` publishes a port. The ingress is on `edge` only; PostgreSQL and RabbitMQ are on `private` only.
- Every service binds its private listener to its own `private` address (`INTERNAL_HOST`), so the ingress cannot
  connect to it even though both are on `edge` (`make security-test` checks all six).
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
| `credentials/<caller>-to-<receiver>` (14) | the caller's outbound credential and the receiver's inbound credential for that caller |
| `db/<service>.password` (4), `postgres_superuser` | PostgreSQL roles |
| `env/<service>.env` | `DATABASE_URL`, and for Auth `JWT_SECRET`, for Stats `RABBITMQ_URL` |
| `rabbitmq.conf` | the broker account |

The 14 pairs: `auth-service` to `user-service`, `game-session`, `game-gateway`, `notification-gateway`;
`user-service` to `auth-service`, `game-gateway`, `notification-gateway`, `stats-service`, `game-session`;
`game-session` to `user-service`; `stats-service`, `game-gateway`, `notification-gateway` to `auth-service`;
`game-gateway` to `game-session`. A credential works for one receiver and the scopes that receiver grants that
caller; there is no shared secret.

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

`make security-test` runs 106 checks against the running stack: TLS versions and headers, cookie attributes,
CSRF/CORS/WebSocket origin, private listeners unreachable from the ingress, credentials and scopes, injection and
oversized input, rate limits, profile IDOR, container and database least privilege, and no secret in any log or
tracked file. It restarts Auth first because rate-limit counters live in memory.

## Real certificates

`make up` creates a self-signed certificate (`certs/`), which browsers warn about. For a real deployment put a CA
certificate at `certs/server.crt` and `certs/server.key` (the same file names) and set `PUBLIC_BASE_URL` in `.env`
to the public `https://` origin: the gateways refuse any other `Origin`, and Auth builds the Google redirect URI
from it. Google sign-in is off until `GOOGLE_CLIENT_ID` and `GOOGLE_CLIENT_SECRET` are set.

## Known gaps (stated, not hidden)

| Gap | Effect | Owner |
| --- | --- | --- |
| RabbitMQ topology and event names (#189, #171, #141) | Stats' consumer reconnects with backoff and logs `no queue 'stats.game-events'`; no event flows, and room events never reach the room socket. | zehraxaa |
| Web BFF (#88) not started | Game Session REST is deliberately not routed: it takes the caller from `X-User-Id`, which only the BFF may set. | ygtdmr |
| REST services have not adopted the shared origin guard (#2) | The ingress enforces the contract's cross-site rule; the services behind it do not. | service owners |
| Auth has no health route (#85 adoption) | Readiness of Auth is probed with a request that must answer 401. | ygtdmr |
| `DATABASE_URL`, `JWT_SECRET`, `RABBITMQ_URL` come from the environment (#157 F4) | Visible to anyone who can run `docker inspect` on the host. | service owners |
| One instance of each service | The Game Gateway contract assumes a single instance. | contract |

## Repository access (issue #191, handover item)

Checked 2026-11-27 with `gh api repos/ygtdmr/ft_transcendence/collaborators`: four collaborators (`ygtdmr` admin;
`guryelf`, `zehraxaa`, `BercemES` write). The former members UmutSogukpinar and Melovii are not listed. Deploy
keys, webhooks and Actions secrets need admin rights to read and were not checked: the repository admin has to
confirm that none was created by a former member, and that any credential they could see has been rotated. This
document changes no permission.
