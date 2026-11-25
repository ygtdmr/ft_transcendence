# Notification Gateway

Holds every signed-in user's notification socket
(`GET /ws/v2/notifications`, contract `collections/v2/notification-websocket`)
and the in-memory socket registry that User Service reads for presence. It holds
no domain data and makes no authorization decision beyond session validity.

## Handshake

The checks run in this order, before any upgrade:

1. **Origin.** The request needs exactly one `Origin`, equal to `PUBLIC_ORIGIN`.
   Anything else is `403 ORIGIN_NOT_ALLOWED`, and the cookie is never looked at.
2. **Upgrade.** A request that isn't a WebSocket upgrade is `400 VALIDATION_ERROR`.
3. **Session.** The credential is the `ft_session` cookie only (no query
   token, header or `ft_refresh`). Auth Service validates it (`session.validate`):
   - an invalid session is `401 UNAUTHORIZED`;
   - an Auth outage is `503 SERVICE_UNAVAILABLE`, never a logout;
   - a refused gateway credential is a configuration fault, answered `500`.
4. **Registration.** The socket is registered under `user_id` and `session_id`,
   and `notifications.ready` is sent first.

## On an open socket

- **Client frames.** The socket accepts no commands. Every client frame gets
  `{"type":"error","payload":{"code":"INVALID_EVENT"}}` and the socket stays
  open. More than 10 frames in a minute closes it with `1008`. A frame over
  4 KiB closes it with `1009`.
- **Multi-tab.** Each tab is its own socket. An event reaches every open socket
  of the user.
- **Slow clients.** A socket whose send queue (`SOCKET_SEND_QUEUE`) is full is
  closed with `1013` instead of being waited on.
- **Keep-alive.** The gateway pings every `SOCKET_PING_INTERVAL`. A connection
  silent for `SOCKET_PONG_WAIT` is closed.
- **Shutdown.** Readiness fails, then every socket is closed with
  `1001 GOING_AWAY`. A restart starts with an empty registry, and clients
  reconnect.

## Private routes (private listener only)

| Route | Scope, caller | Behaviour |
| --- | --- | --- |
| `GET /internal/v1/connections?user_id=…` | `connections.read`, user-service | Answers which of the 1–100 distinct requested users hold an open socket on this instance, ascending, from memory only. Errors: `401`/`403` for the service credential, `422` (field `user_id`) for the query. |
| `PUT /internal/v1/session-revocations/{session_id}` | `session.revocation.notify`, auth-service | Body `{"user_id": positive, "cause": "SESSION_REVOKED" \| "ACCOUNT_DELETED"}`. Every local socket of the session gets `UNAUTHORIZED`, then a `4401` close whose reason is the cause. Other sessions stay open. Always `204`, also for an unknown session or a repeat. For 15 minutes the session can't register a new socket here, which closes the race with a handshake Auth validated just before the revocation. |

## Not done yet

- **Delivering User Service social/profile events from RabbitMQ.** Three things block it:
  - the exchange, queue and message format (#189, #141);
  - a stable `event_id` in User's outbox;
  - how `is_online` gets into the `friend` entries.

  The registry already fans out to every tab (`socketregistry.Registry.SendToUser`).
- **Contract work:** the Bruno request file for `session.revocation.notify`.

## Configuration

See `.env.example`. Secrets are file paths only, with each value at least 32 bytes.
An invalid configuration stops startup and lists every problem by variable
name, never by value.

## Build and test

From `backend/`:

```
cd notification-gateway && gofmt -l . && GOWORK=off go vet ./... && GOWORK=off go test -race -count=1 ./...
docker build -f notification-gateway/Dockerfile .
```

The tests run the real shared session client against a stub Auth Service and
drive real WebSocket connections.
