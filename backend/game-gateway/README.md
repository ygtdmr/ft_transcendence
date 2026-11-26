# Game Gateway

Holds the room sockets (`GET /ws/v2/rooms/{room_id}`, contract
`collections/v2/game-websocket`) and talks to Game Session over its private
`room.socket` routes (`collections/internal/v1/service-rest-api/09 - Game Gateway Calls`).

The gateway holds no room or game data and makes no membership decision.
Game Session owns rooms, membership and every command; the gateway forwards
messages to it and writes back what it answers.

## Handshake

The checks run in this order, before any upgrade:

1. **Origin.** Exactly one `Origin` equal to `PUBLIC_BASE_URL`, else a bare
   `403`. No cookie is examined.
2. **Upgrade.** A request that isn't a WebSocket upgrade is `400 VALIDATION_ERROR`.
3. **Session.** The credential is the `ft_session` cookie only (no query token,
   header or `ft_refresh`), validated by Auth:
   - an invalid session is `401 UNAUTHORIZED`;
   - an Auth outage is `503`, never a logout.
4. **Room id.** A positive integer, else `404 ROOM_NOT_FOUND`.
5. **Report socket opened** (`PUT /internal/v1/rooms/{room_id}/connections/{user_id}`).
   - Game Session's verdict refuses the handshake with the same status and
     code: `403 NOT_ROOM_MEMBER`, `404 ROOM_NOT_FOUND`, `409 INVALID_ROOM_STATE`.
   - Any other failure is `503 SERVICE_UNAVAILABLE`, never a membership verdict.
6. **Upgrade.** If the upgrade then fails, the member's "last socket closed" is
   reported.

Until Game Session has acknowledged **Report gateway started**
(`DELETE /internal/v1/room-connections`, sent at startup and retried with
backoff), every handshake gets `503` and readiness fails.

## On an open socket

- **Commands.** Every text message goes to Game Session unchanged
  (`POST /internal/v1/rooms/{room_id}/commands`, `X-User-Id` and a lowercase
  `X-Session-Id` from the validated session).
  - One socket's messages are forwarded one at a time, in order, retries
    included.
  - The reply is written to that socket only.
  - If a call still fails after its retries, nothing is written. The client
    may repeat the same `request_id`, and Game Session's ledger answers it once.
- **Closures in a reply.** They close every room socket of the named member with
  the given code (`4403`, `4404`). When the sender is that member, its reply is
  written first.
- **Other messages.** An empty message is ignored, a binary message closes with
  `1003`, and one over 64 KiB closes with `1009`.
- **Multi-tab.** Each tab is a socket; none adds a member. Only the member's
  last room socket on this instance reports "last socket closed". One member's
  reports are serialized, so a late disconnect can never overtake a newer
  connection.
- **Slow clients.** A full send queue closes the socket with `1013`.
- **Keep-alive.** Pings go out every `SOCKET_PING_INTERVAL`. A connection silent
  for `SOCKET_PONG_WAIT` is closed.
- **Shutdown.** Readiness fails, then every socket closes with
  `1001 GOING_AWAY` and each member's last socket is reported. A restart sends
  Report gateway started again.

This revision runs as a single Game Gateway instance (contract, folder `09`).

## Private routes (private listener only)

| Route | Scope, caller | Behaviour |
| --- | --- | --- |
| `GET /internal/v1/connections?user_id=…` | `connections.read`, user-service | Which of the 1–100 requested users hold an open room socket here, ascending |
| `PUT /internal/v1/session-revocations/{session_id}` | `session.revocation.notify`, auth-service | Body `{user_id, cause}`. The session's sockets get the `UNAUTHORIZED` error, then a `4401` close with the cause as reason. Other sessions stay open. Always `204`. For 15 minutes the session can't register a new socket here. |

## Not done yet

- **Room events from Game Session's outbox** (`room.state` after a connect,
  game events, `socket.close` for kicks and leaves) arrive over RabbitMQ.
  That needs the topology and message format (#189, #171, zehraxaa). The
  registry already delivers per room and per member
  (`socketregistry.Registry.SendToUserIn`, `CloseUserIn`).
- **Game Session doesn't serve the `room.socket` routes yet.** The tests use a
  stub that implements the contract.

## Build and test

From `backend/`:

```
cd game-gateway && gofmt -l . && GOWORK=off go vet ./... && GOWORK=off go test -race -count=1 ./...
docker build -f game-gateway/Dockerfile .
```
