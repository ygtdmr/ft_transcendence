# Web BFF

The Web BFF is the single entry point for browser REST traffic. The reverse
proxy sends `/api/v1/*` and `/api/v2/*` here; room and notification sockets go
to the gateways directly. The BFF owns no data: it checks each request, then
forwards it to the service that owns the route.

## What it enforces

- **Explicit routes.** `internal/routes` lists every forwarded route with its
  owning service. Anything else, including `/api/v1/rooms` and `/internal/*`,
  is answered `404 NOT_FOUND` by the BFF and reaches no service. A known path
  with another method is `405 METHOD_NOT_ALLOWED`.
- **Origin first.** For cookie-authenticated `POST`, `PUT`, `PATCH` and
  `DELETE` requests (every session route and `POST /api/v1/auth/refresh`), a
  foreign `Origin` or a cross-site `Sec-Fetch-Site` is refused with
  `403 FORBIDDEN_ORIGIN` before the session is examined.
- **Cookie-only sessions.** Session routes read `ft_session` from the cookie
  only and validate it through Auth's private `GET /internal/v1/sessions/current`.
  A missing, invalid, expired or revoked session is `401 UNAUTHORIZED`; an Auth
  outage is `503 SERVICE_UNAVAILABLE`, never a logout. A credential in a query
  parameter, a JSON body or a header does not authenticate.
- **Public sign-in routes.** Registration, login and Google sign-in are
  forwarded without a session; refresh is verified by Auth through
  `ft_refresh`.
- **Identity headers.** `Authorization`, `Proxy-Authorization`, `X-User-Id` and
  `X-Session-Id` from the browser never reach a service. Game Session receives
  `X-User-Id` set by the BFF from the validated session, and no cookies.
- **Correlation.** Each service receives the BFF's `X-Request-ID`, a child
  `Traceparent` and the `X-Forwarded-For` chain with the reverse proxy's
  address appended. Services resolve the client through `TRUSTED_PROXIES`.
- **Failures.** An unreachable service is `503 SERVICE_UNAVAILABLE`. Logs carry
  the request ID, method, path and status, never cookies, query strings or
  service addresses.

`GET /api/v1/health` is answered by the BFF itself with the REST v1 contract
version.

## Routes

| Owner | Routes |
| --- | --- |
| Auth Service | `/api/v1/auth/register`, `/login`, `/refresh`, `/oauth/google/authorize`, `/oauth/google/callback`, `GET`/`DELETE /api/v1/auth/session` |
| Stats | `GET /api/v1/users/me/matches` |
| User Service | `/api/v1/users/me` (`GET`, `PATCH`, `DELETE`), `/api/v1/users/me/avatar` (`POST`, `PUT`), `/api/v1/avatars/presets`, `/api/v1/users/search`, `/api/v1/users/{user_id}`, `/reports`, `/block`, `/api/v1/friends`, `/api/v1/friends/requests/*` |
| Game Session | `POST /api/v2/rooms`, `GET /api/v2/rooms/lookup/{room_code}`, `GET /api/v2/rooms/{room_id}`, `POST /api/v2/rooms/{room_id}/members`, `DELETE /api/v2/rooms/{room_id}/members/me` |

`GET /api/v2/rooms/{room_id}/presence` is not served yet.

## Configuration

See `.env.example`. Settings follow `backend/shared/SKELETON.md`.

| Variable | Default | Meaning |
| --- | --- | --- |
| `PORT` | `8080` | Public listener. |
| `PUBLIC_BASE_URL` | required | The public origin, such as `https://host:8443`. |
| `AUTH_INTERNAL_URL`, `AUTH_SERVICE_OUTBOUND_CREDENTIAL_FILE`, `AUTH_INTERNAL_TIMEOUT` | required | Auth's private listener, the BFF's `session.validate` credential file and the call timeout. |
| `AUTH_PUBLIC_URL`, `USER_PUBLIC_URL`, `STATS_PUBLIC_URL`, `GAME_PUBLIC_URL` | required | Each service's public listener origin, without a path. |
| `UPSTREAM_TIMEOUT` | `10s` | Upper bound for a service's response headers (1s to 1m). |
| `APP_ENV`, `LOG_FORMAT`, `LOG_LEVEL`, `SHUTDOWN_*` | shared defaults | Common settings. |

## Deployment notes

- Game Session trusts `X-User-Id` on its public listener. Only the BFF may reach
  that listener: keep it off the reverse proxy's network.
- Add the BFF's address to `TRUSTED_PROXIES` in Auth and User so their rate
  limits see the browser's address.

## Development

```sh
GOWORK=off go build ./...
GOWORK=off go vet ./...
GOWORK=off go test ./...
docker build -f web-bff/Dockerfile .   # from backend/
```
