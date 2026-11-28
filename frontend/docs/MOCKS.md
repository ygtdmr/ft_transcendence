# Mock System

The mock system lets the whole frontend run with no backend. It replaces the REST
transport and the WebSocket transport with in-memory servers that follow the Bruno
contracts: the same paths, envelopes, event types, error codes and room rules.

Back to the [Frontend Developer Guide](../README.md). Test recipes:
[Testing Guide](TESTING.md).

## Enabling the Mocks

In `frontend/.env`:

```dotenv
VITE_MOCK_API=true          # mock REST
VITE_MOCK_SOCKETS=true      # mock room and notification sockets
# VITE_MOCK_AUTH=logged-in  # logged-in | logged-out | in-room | session-expired
# VITE_MOCK_ROOM_SCENARIO=room-waiting
```

Restart `npm run dev` after every change. On startup the console logs:

```text
[mock api] installed (auth: logged-in, latency: 250ms) — control it with `mockApi` in the console. Seeded accounts use the password 'codenames42'.
[mock sockets] installed (default room scenario: room-waiting; available: …) — control them with `mockSockets` in the console.
```

Use both mocks together. They share one room registry, so a room created or joined
over mock REST is the room its mock socket connects to.

## Architecture

| Area                  | Implementation                                                                                    | Location                                      |
| --------------------- | ------------------------------------------------------------------------------------------------- | --------------------------------------------- |
| Installation          | Dynamic imports guarded by `import.meta.env.DEV` and the `VITE_MOCK_*` flags                      | `src/main.tsx`                                |
| REST transport seam   | `setRequestTransport()` replaces `fetch` under `apiRequest`                                       | `src/shared/api/client.ts`                    |
| Mock REST server      | Route table for every REST binding: auth, profile, avatars, friends, rooms                         | `src/shared/api/mock/mockApiServer.ts`        |
| REST config           | Latency, initial session, forced outcomes                                                         | `src/shared/api/mock/config.ts`               |
| Seed data             | Accounts, friends, seeded rooms, match history, avatar presets                                    | `src/shared/api/mock/fixtures.ts`             |
| Socket transport seam | `setTransportFactory()` replaces the browser `WebSocket`                                          | `src/shared/websocket/transport.ts`           |
| Mock socket           | Delivers frames, simulates drops and reconnects                                                   | `src/shared/websocket/mock/mockTransport.ts`  |
| Room and game server  | One `MockRoomServer` per room: lobby, countdown, game, staffing pause, post-game, grace periods   | `src/shared/websocket/mock/mockRoomServer.ts` |
| Notification server   | Social and profile events pushed to the notification socket                                       | `src/shared/websocket/mock/mockNotificationServer.ts` |
| Scenarios             | Ready-made room states reached through real moves                                                 | `src/shared/websocket/mock/scenarios.ts`      |
| Shared registry       | Rooms, network state, session check, user directory                                               | `src/shared/websocket/mock/registry.ts`       |

Behavior:

| Question                  | Answer                                                                                                                                                             |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Where is state stored?    | JavaScript memory of the page                                                                                                                                      |
| Does it survive a reload? | No. A reload restores the seed data and the `.env` settings                                                                                                        |
| Delays                    | Every REST response waits `mockApi.config.latencyMs` (default `250`). Socket frames are asynchronous with no fixed delay                                           |
| Errors                    | Forced REST outcomes (`mockApi.config.outcomes`); real validation errors from the route handlers; `MockActionError` for illegal socket moves |
| Multiple players          | Other players are simulated from the console (`playerJoin`, `selectRole`, `submitClue`, `guess`, …)                                                                |
| Disconnect and reconnect  | `dropConnection()`, `offline()` / `online()`; recovery always uses a fresh `room.state` snapshot                                                                   |
| Timers                    | Countdown, grace periods (`graceMs`), post-game window (`postGameMs`), staffing deadline (`staffingMs`) run in real time                                           |

## Seeded Data

### Accounts

Every account uses the password `codenames42`. Emails replace `_` with `.` in the
username and end in `@example.com`. These are mock-only values; they do not exist on
the backend.

| Id  | Username        | Email                       | Online | Notes                                             |
| --- | --------------- | --------------------------- | ------ | ------------------------------------------------- |
| 42  | `player_one`    | `player.one@example.com`    | yes    | Default signed-in user                            |
| 43  | `red_agent`     | `red.agent@example.com`     | yes    | Friend; mock bot                                  |
| 44  | `blue_master`   | `blue.master@example.com`   | yes    | Friend; mock bot                                  |
| 45  | `blue_agent`    | `blue.agent@example.com`    | no     | Friend (offline); mock bot                        |
| 46  | `extra_red`     | `extra.red@example.com`     | yes    | In `FULL44`; mock bot                             |
| 47  | `night_owl`     | `night.owl@example.com`     | no     | In `FULL44`; mock bot (`lateJoiner`)              |
| 48  | `word_wizard`   | `word.wizard@example.com`   | yes    | In `QWER12` (BLUE Spymaster, host)                |
| 49  | `redacted_rita` | `redacted.rita@example.com` | no     | In `QWER12` (RED Operative)                       |
| 50  | `clue_crafter`  | `clue.crafter@example.com`  | yes    | In `FULL44`                                       |
| 51  | `silent_scout`  | `silent.scout@example.com`  | yes    | In `FULL44`                                       |
| 52  | `lucky_guess`   | `lucky.guess@example.com`   | yes    | In `BUSY77` (BLUE Spymaster)                      |
| 53  | `card_shark`    | `card.shark@example.com`    | yes    | In `BUSY77` (RED Spymaster, host)                 |
| 54  | `mind_reader`   | `mind.reader@example.com`   | yes    | In `BUSY77` (RED Operative)                       |
| 55  | `echo_ops`      | `echo.ops@example.com`      | yes    | In `BUSY77` (BLUE Operative)                      |

Log in as any account to see the app from that user's side. Only `player_one` has
friends and match history.

### Rooms

| Code     | Id   | Status                 | Members                                                  | Purpose                                                     |
| -------- | ---- | ---------------------- | -------------------------------------------------------- | ----------------------------------------------------------- |
| `QWER12` | 1001 | `WAITING`, 2 of 8      | `word_wizard`, `redacted_rita`                           | Join a lobby with free seats; `in-room` session starts here |
| `FULL44` | 1002 | `WAITING`, 4 of 4      | `extra_red`, `night_owl`, `clue_crafter`, `silent_scout` | Full room: join is refused with `ROOM_FULL`                 |
| `BUSY77` | 1003 | `IN_GAME`, BLUE starts | `card_shark`, `mind_reader`, `lucky_guess`, `echo_ops`   | Running match: join is refused until a player leaves it     |

Rooms created over mock REST get ids from `1004`. A room socket that connects to an id
the mock REST server does not know starts in `VITE_MOCK_ROOM_SCENARIO`.

### Mock Bots

The room scenarios and `configureStartable()` use these players (`MOCK_BOTS` in
`mockRoomServer.ts`):

| Key          | Id  | Username      |
| ------------ | --- | ------------- |
| `redAgent`   | 43  | `red_agent`   |
| `blueMaster` | 44  | `blue_master` |
| `blueAgent`  | 45  | `blue_agent`  |
| `extraRed`   | 46  | `extra_red`   |
| `lateJoiner` | 47  | `night_owl`   |

### Social and Statistics

- **Friends of `player_one`:** `red_agent` and `blue_master` (online), `blue_agent`
  (offline). None of them is in a room.
- **Friend requests:** `clue_crafter` (50) has sent `player_one` a request, waiting for
  Accept or Decline. Nobody is blocked.
- **Match history:** five finished matches of `player_one` (`fixtures.ts`,
  `SEED_MATCHES`), with wins, losses and an assassin loss.
- **Avatars:** the 11 presets bundled in `src/assets/avatars`. Uploads accept images up
  to 2 MB.
- **Profile:** `player_one` is level 7, 18 wins, 11 losses, `penalty_points` 0.

### Session States (`VITE_MOCK_AUTH`)

| Value             | Start state                                   | Tests                             |
| ----------------- | --------------------------------------------- | --------------------------------- |
| `logged-in`       | `player_one` signed in                        | Normal work                       |
| `logged-out`      | No session                                    | Landing page, Log In, Sign Up     |
| `in-room`         | `player_one` signed in and member of `QWER12` | Route recovery into the room      |
| `session-expired` | Access cookie expired, refresh still valid    | Silent refresh on the first `401` |

## Console API: `mockApi`

Available when `VITE_MOCK_API=true`.

| Command                                               | Purpose                                                       |
| ----------------------------------------------------- | ------------------------------------------------------------- |
| `mockApi.config.latencyMs = 1000`                     | Delay of every REST response, in ms                           |
| `mockApi.config.outcomes.<endpoint> = '<outcome>'`    | Force the result of one endpoint                              |
| `mockApi.config.outcomes = {}`                        | Remove all forced outcomes                                    |
| `mockApi.config.auth = 'logged-out'; mockApi.reset()` | Change the session state; reload to see it                    |
| `mockApi.reset()`                                     | Restore seed data and apply `config.auth` (reload afterwards) |
| `mockApi.server.expireAccess()`                       | Access cookie expires; the next `401` refreshes silently      |
| `mockApi.server.revokeSession()`                      | Access and refresh both fail; the app returns to Log In       |
| `mockApi.uninstall()`                                 | Restore the real `fetch` transport                            |

### Forced Outcomes

Outcomes: `success`, `validation-error`, `unauthorized`, `forbidden`, `not-found`,
`conflict`, `rate-limited`, `server-error`, `network-error`.

`rate-limited` (429), `server-error` (503) and `network-error` work on every endpoint.
`unauthorized` works on every endpoint that needs a session. The others work only where
the contract documents them; otherwise the console warns and the call succeeds.

| Endpoint key           | Documented outcomes                                      |
| ---------------------- | -------------------------------------------------------- |
| `register`             | `validation-error`, `conflict`                           |
| `login`                | `unauthorized`, `validation-error`                       |
| `refresh`              | `unauthorized`                                           |
| `updateOwnProfile`     | `validation-error`, `conflict`                           |
| `deleteOwnAccount`     | `validation-error`                                       |
| `reportUser`           | `not-found`, `conflict`, `validation-error`              |
| `getPublicProfile`     | `not-found`, `validation-error`                          |
| `uploadAvatar`         | `validation-error`                                       |
| `selectAvatarPreset`   | `validation-error`                                       |
| `sendFriendRequest`    | `forbidden`, `not-found`, `conflict`, `validation-error` |
| `acceptFriendRequest`  | `not-found`                                              |
| `declineFriendRequest` | `not-found`                                              |
| `cancelFriendRequest`  | `not-found`                                              |
| `blockUser`            | `not-found`, `validation-error`                          |
| `unblockUser`          | `not-found`                                              |
| `removeFriend`         | `not-found`                                              |
| `createRoom`           | `conflict`, `validation-error`                           |
| `lookupRoom`           | `not-found`, `validation-error`                          |
| `joinRoom`             | `not-found`, `conflict`, `validation-error`              |
| `getRoom`              | `not-found`, `forbidden`, `validation-error`             |
| `leaveRoom`            | `not-found`, `forbidden`                                 |
| `openDirectChannel`    | `forbidden`, `not-found`, `validation-error`             |
| `messageHistory`       | `forbidden`, `not-found`                                 |

Endpoints with no documented error: `health`, `session`, `endSession`,
`getOwnProfile`, `searchUsers`, `matchHistory`, `listAvatarPresets`, `listFriends`,
`listFriendRequests`, `listChannels`.

Example:

```js
mockApi.config.outcomes.joinRoom = 'conflict'; // next Join answers 409
mockApi.config.outcomes.login = 'server-error'; // Log In shows the service error
```

## Console API: `mockSockets`

Available when `VITE_MOCK_SOCKETS=true`. Most commands need an open room page:
`mockSockets.room()` returns the server of the last room that connected.

Every room method applies the same rules as the server. An illegal move throws a
`MockActionError` with the contract code (`ROOM_FULL`, `ROLE_CONFLICT`,
`NOT_YOUR_TURN`, …) and changes nothing.

### Scenarios

```js
mockSockets.scenario('operative-turn');
```

Replaces the current room with a fresh room in that scenario. The room keeps its code,
the socket reconnects and the page receives the new `room.state`. `player_one` sits on
RED, or joins a paused match to take its free seat.

| Scenario         | State                                                                                                  |
| ---------------- | ------------------------------------------------------------------------------------------------------ |
| `empty`          | Only you, host, in `WAITING`                                                                           |
| `room-waiting`   | `red_agent` (RED Operative, ready), `blue_master` (BLUE Spymaster, ready), `blue_agent` still unseated |
| `room-full`      | Capacity 4, all seats taken                                                                            |
| `countdown`      | Both teams staffed and ready; the 3-second countdown runs, then the game starts. You are RED Operative |
| `spymaster-turn` | Game running, RED to give a clue. You are RED Spymaster                                                |
| `operative-turn` | RED Spymaster gave `sea 2`. You are RED Operative and can guess                                        |
| `opponent-turn`  | BLUE started and gave `trees 2`. You are RED Operative and wait                                        |
| `game-over`      | RED revealed all nine cards; result screen with RED winning                                            |
| `assassin-loss`  | You are RED Operative; the assassin was revealed; RED loses                                            |
| `paused`         | RED's Spymaster left mid-match; game paused, staffing countdown runs                                   |
| `seat-claim`     | Bots play; RED's last Operative disconnected; you joined the paused match and can take the seat        |

### Room Methods

| Command                                                                 | Effect                                                                                                                                                                                        |
| ----------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `mockSockets.room().playerJoin({ user_id: 47, username: 'night_owl' })` | A player joins unseated. In a lobby a countdown stops; a running match refuses the join unless it is paused                                                                                   |
| `mockSockets.room().playerLeave(43)`                                    | A member leaves (`EXITED`). Optional second argument: `SESSION_ENDED`, `DISCONNECTED`, `POST_GAME_TIMEOUT`, `KICKED_BY_HOST`, `ACCOUNT_DELETED`. Mid-match it may pause the game for staffing |
| `mockSockets.room().selectTeam(43, 'BLUE')`                             | A member picks a team                                                                                                                                                                         |
| `mockSockets.room().selectRole(47, 'OPERATIVE', 'BLUE')`                | A member claims a team and role together; mid-match only an unseated joiner of a paused game may                                                                                              |
| `mockSockets.room().setReady(43, true)`                                 | A member toggles ready                                                                                                                                                                        |
| `mockSockets.room().updateSettings(6)`                                  | Host changes capacity                                                                                                                                                                         |
| `mockSockets.room().setTurnTimer(60)`                                   | Host sets the turn timer: `null`, `60`, `90` or `120`                                                                                                                                         |
| `mockSockets.room().submitClue('ocean', 2)` with a board word           | Refused with `INVALID_CLUE` (`WORD_ON_BOARD`); nothing changes                                                                                                                                |
| `mockSockets.room().expireTurn()`                                       | The active turn runs out of time now; the turn passes to the other team                                                                                                                       |
| `mockSockets.room().kick(43)`                                           | Host removes a member. `kick(42)` kicks you when a bot is host                                                                                                                                |
| `mockSockets.room().configureStartable('SPYMASTER')`                    | Fill both teams, ready everyone, start the countdown. Argument is your RED role                                                                                                               |
| `mockSockets.room().startGame('BLUE')`                                  | Skip the countdown; start with that team                                                                                                                                                      |
| `mockSockets.room().submitClue('sea', 2)`                               | Active team's Spymaster gives a clue                                                                                                                                                          |
| `mockSockets.room().guessCard(5)`                                       | Active team's Operative guesses card id `5`                                                                                                                                                   |
| `mockSockets.guess('NEUTRAL')`                                          | Guess the first unrevealed card of `RED`, `BLUE`, `NEUTRAL` or `ASSASSIN`                                                                                                                     |
| `mockSockets.room().passTurn()`                                         | Active team ends its guessing                                                                                                                                                                 |
| `mockSockets.room().endGame('BLUE')`                                    | End the match now; second argument `'ASSASSIN_REVEALED'` for an assassin end                                                                                                                  |
| `mockSockets.room().returnToLobby(43)`                                  | A player leaves the result screen for the lobby                                                                                                                                               |
| `mockSockets.room().state()`                                            | Current room as the local player sees it                                                                                                                                                      |
| `mockSockets.room().resync()`                                           | Send a fresh `room.state`                                                                                                                                                                     |

Timers (change before the event that starts them):

```js
mockSockets.room().staffingMs = 15000; // staffing shutdown deadline (default 120000)
mockSockets.room().postGameMs = 15000; // result screen decision window (default 60000)
mockSockets.room().graceMs.game = 10000; // seat hold after a drop in a match (default 60000)
mockSockets.room().graceMs.room = 10000; // seat hold after a drop in the lobby (default 30000)
```

### Connection Methods

| Command                                   | Effect                                                                                     |
| ----------------------------------------- | ------------------------------------------------------------------------------------------ |
| `mockSockets.room().dropConnection(3000)` | Room socket drops, reconnects after 3 s, receives a fresh `room.state`                     |
| `mockSockets.offline(20000)`              | Network down for 20 s: sockets drop and REST calls fail. Without an argument it stays down |
| `mockSockets.online()`                    | Network back; sockets reconnect                                                            |
| `mockSockets.notifications.dropConnection(3000)` | Notification socket drops and reconnects; friends and requests are read again |

### Social Methods (`mockApi.server`)

| Command                                                                                   | Effect                                                          |
| ----------------------------------------------------------------------------------------- | --------------------------------------------------------------- |
| `mockApi.server.friendRequestFrom(46)`                                                    | `extra_red` sends you a friend request                          |
| `mockApi.server.acceptFriendRequestBy(46)`                                                | `extra_red` accepts the request you sent; you become friends    |
| `mockApi.server.declineFriendRequestBy(46)`                                               | `extra_red` declines the request you sent                       |
| `mockApi.server.cancelFriendRequestBy(46)`                                                | `extra_red` withdraws the request they sent you                 |
| `mockApi.server.blockedBy(44)`                                                            | `blue_master` blocks you; `blockedBy(44, false)` lifts it       |
| `mockApi.server.profileUpdateBy(43, { username: 'agent_red' })`                           | `red_agent` renames; `avatar_url` changes the avatar            |

### Events the Mocks Emit

Room socket: `room.state`, `room.player.joined`, `room.player.left`,
`room.player.updated`, `room.settings.updated`, `room.countdown.started`,
`room.countdown.tick`, `room.countdown.cancelled`, `room.post_game.started`,
`room.player.returned_to_lobby`, `room.post_game.completed`, `room.closed`,
`game.started`, `game.clue.submitted`, `game.card.revealed`, `game.score.updated`,
`game.turn.changed`, `game.staffing.required`, `game.staffing.restored`,
`game.ended`, `game.cancelled`, `game.history.appended`.

Notification socket: `notifications.ready`, `friend.request.received`,
`friend.request.resolved`, `friend.removed`, `friend.restored`, `user.profile.updated`.

## Resetting Mock State

| Need                          | Action                                                            |
| ----------------------------- | ----------------------------------------------------------------- |
| Back to the seed              | Reload the page                                                   |
| Other session state           | Change `VITE_MOCK_AUTH`, restart `npm run dev`                    |
| Reset without changing `.env` | `mockApi.config.auth = 'logged-in'; mockApi.reset()`, then reload |
| Clear forced errors only      | `mockApi.config.outcomes = {}`                                    |
| Fresh room state              | `mockSockets.scenario('<name>')`                                  |

The mocks write nothing to browser storage. The only stored key is the language,
`localStorage["ft_transcendence.language"]`.

## Production Safety

- `src/main.tsx` returns before importing any mock unless `import.meta.env.DEV` is true.
  Vite replaces that constant at build time, so the mock modules are not in `dist/`.
- `mockApi` and `mockSockets` are created only by the install functions.
- The `/ui` primitive gallery route is added only when `import.meta.env.DEV` is true.
- The seeded password exists only in mock code.
