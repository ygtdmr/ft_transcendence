# Frontend Developer Guide

React single-page application for Codenames Online. This guide covers running the
frontend locally, the development and mock modes, and how to test every supported
flow without a backend.

Architecture, styling rules and contract sources are described in the
[repository README](../README.md). Detailed references:

- [Mock System](docs/MOCKS.md) — mock architecture, seeded data and the console API.
- [Testing Guide](docs/TESTING.md) — step-by-step manual test scenarios.

## Quick Start

Requirements: Node.js and npm. The repository pins no Node.js version; use a version
supported by Vite 7 (Node.js 20.19+ or 22.12+). The package manager is npm
(`package-lock.json`).

Frontend only, with no backend (recommended for UI work):

```bash
cd frontend
npm install
cp .env.example .env
```

Then uncomment these lines in `.env`:

```dotenv
VITE_MOCK_API=true
VITE_MOCK_SOCKETS=true
```

```bash
npm run dev
```

Open <http://localhost:5173> (the Vite default; Vite prints another port when 5173 is
busy). You are logged in as `player_one`. Every seeded account uses the password
`codenames42`.

Stop the server with `Ctrl+C`. Mock state lives in memory only: a page reload restores
the seed data (see [Resetting mock state](docs/MOCKS.md#resetting-mock-state)).

## Development Modes

| Mode                | How to activate                                                      | REST                                | Room and notification sockets       | Use                                                                  |
| ------------------- | -------------------------------------------------------------------- | ----------------------------------- | ----------------------------------- | -------------------------------------------------------------------- |
| Backend development | `npm run dev`, mock variables unset                                  | Real Gateway through the Vite proxy | Real Gateway through the Vite proxy | Integration with the backend                                         |
| Full mock           | `npm run dev` with `VITE_MOCK_API=true` and `VITE_MOCK_SOCKETS=true` | In-memory mock                      | In-memory mock                      | Frontend work with no backend                                        |
| Sockets-only mock   | `npm run dev` with only `VITE_MOCK_SOCKETS=true`                     | Real Gateway                        | In-memory mock                      | Real accounts, simulated rooms                                       |
| REST-only mock      | `npm run dev` with only `VITE_MOCK_API=true`                         | In-memory mock                      | Real Gateway                        | Rarely useful: the mock session cookie does not exist on the Gateway |
| Production build    | `npm run build`, then `npm run preview`                              | Same origin `/api`                  | Same origin `/ws`                   | Checking the shipped bundle                                          |

Details:

- **Backend development.** Vite proxies `/api` and `/ws` to `VITE_DEV_PROXY_TARGET`
  (default `http://localhost:3000`). The browser sees one origin, so the HttpOnly
  `ft_session` cookie reaches REST calls and WebSocket upgrades. The backend must be
  running.
- **Mock modes.** `src/main.tsx` installs the mocks before the first render. Each mock
  is a dynamic import guarded by `import.meta.env.DEV`, so it runs only under
  `npm run dev`. The mocks replace the transport only: the same API bindings, stores,
  reconnect logic and components run in every mode.
- **Production build.** `VITE_MOCK_*` variables are ignored. The mock code, the `/ui`
  gallery and the `mockApi` / `mockSockets` console objects are not in the bundle.
  `npm run preview` has no proxy, so `/api` and `/ws` calls fail unless a reverse proxy
  serves the build.

Change `.env`, then restart `npm run dev`. Vite reads `.env` only at startup.

## Environment Variables

All variables are optional. `.env.example` lists each one.

| Variable                  | Default                 | Scope                                      | Purpose                                                                       |
| ------------------------- | ----------------------- | ------------------------------------------ | ----------------------------------------------------------------------------- |
| `VITE_DEV_PROXY_TARGET`   | `http://localhost:3000` | Dev server                                 | Gateway address the Vite proxy forwards `/api` and `/ws` to                   |
| `VITE_API_BASE_PATH`      | `/api`                  | All builds                                 | REST prefix. Override only for a non-same-origin setup                        |
| `VITE_WS_BASE_PATH`       | `/ws`                   | All builds                                 | WebSocket prefix. Same rule                                                   |
| `VITE_MOCK_API`           | unset                   | Development only                           | `true` answers every REST call from the mock REST server                      |
| `VITE_MOCK_AUTH`          | `logged-in`             | Development only, with `VITE_MOCK_API`     | Initial mock session: `logged-in`, `logged-out`, `in-room`, `session-expired` |
| `VITE_MOCK_SOCKETS`       | unset                   | Development only                           | `true` replaces the room and notification sockets with the mock servers       |
| `VITE_MOCK_ROOM_SCENARIO` | `room-waiting`          | Development only, with `VITE_MOCK_SOCKETS` | Scenario of a room socket for a room the mock REST server does not know       |

`VITE_*` values are inlined into the bundle. Never put a secret in one. Authentication
uses cookies only; the frontend has no API key, token or OAuth setting.

## Available Commands

Run from `frontend/`.

| Command                | Description                                                    |
| ---------------------- | -------------------------------------------------------------- |
| `npm run dev`          | Vite dev server with hot reload and the `/api` and `/ws` proxy |
| `npm run build`        | Type-check (`tsc -b`) and build into `dist/`                   |
| `npm run preview`      | Serve `dist/` locally                                          |
| `npm run typecheck`    | TypeScript check without output                                |
| `npm run lint`         | ESLint, including the import-direction rules                   |
| `npm run format`       | Prettier: rewrite `src/**/*.{ts,tsx,css}` and root JSON/HTML   |
| `npm run format:check` | Prettier check only                                            |

The project has no automated test runner. Testing is manual, with the mocks. See the
[Testing Guide](docs/TESTING.md).

## Mock Development at a Glance

| Item                                     | Value                                                     |
| ---------------------------------------- | --------------------------------------------------------- |
| Default user                             | `player_one` (id `42`), email `player.one@example.com`    |
| Password of every seeded account         | `codenames42`                                             |
| Waiting room with free seats             | `QWER12`                                                  |
| Full room (4 of 4)                       | `FULL44`                                                  |
| Room with a match running (not joinable) | `BUSY77`                                                  |
| REST console object                      | `mockApi`                                                 |
| Socket console object                    | `mockSockets`                                             |
| Jump to a game state                     | `mockSockets.scenario('operative-turn')` (in a room page) |

Both console objects log a line when they install. Full reference:
[Mock System](docs/MOCKS.md).

## Development Tools

| Tool                     | Access                                       | Purpose                                                           |
| ------------------------ | -------------------------------------------- | ----------------------------------------------------------------- |
| `mockApi`                | Browser console, `VITE_MOCK_API=true`        | Latency, forced REST errors, session expiry, reset                |
| `mockSockets`            | Browser console, `VITE_MOCK_SOCKETS=true`    | Other players, game moves, scenarios, drops, offline network      |
| `/ui`                    | <http://localhost:5173/ui>, development only | Gallery of the shared UI primitives                               |
| WebSocket frame log      | Console, development only                    | Every inbound and outbound frame (`devFlags.logWebSocketTraffic`) |
| REST error log           | Console, development only                    | Normalized REST failures (`devFlags.logApiErrors`)                |
| `[mock api]` debug lines | Console, Verbose level                       | One line per mocked REST call with its status                     |

### Browser DevTools

- **Network.** In mock mode, REST calls and sockets never appear in the Network tab:
  they are answered in memory. Any `/api` or `/ws` request there goes to the real
  Gateway. With the backend, filter on `WS` and open the `/ws/v2/rooms/{id}` or
  `/ws/v2/channels` connection to read frames.
- **Console.** `mockApi` and `mockSockets` live here. Enable the Verbose level to see
  the `[mock api]` lines.
- **Application.** The only key the frontend stores is
  `localStorage["ft_transcendence.language"]` (`en`, `fr` or `tr`). The session lives in
  the HttpOnly `ft_session` and `ft_refresh` cookies, set by the real backend only.

## Troubleshooting

| Symptom                                                         | Likely cause                                                                           | Fix                                                                          |
| --------------------------------------------------------------- | -------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------- |
| `mockApi` or `mockSockets` is `undefined`                       | Variable not exactly `true`, `.env` edited while the server ran, or a production build | Set `VITE_MOCK_API=true` / `VITE_MOCK_SOCKETS=true`, restart `npm run dev`   |
| Console shows proxy errors (`ECONNREFUSED`) for `/api` or `/ws` | A layer is not mocked and no Gateway runs at `VITE_DEV_PROXY_TARGET`                   | Start the backend or enable both mocks                                       |
| Mock mode starts on the Landing page                            | `VITE_MOCK_AUTH=logged-out`                                                            | Log in as `player_one` / `codenames42`, or change the variable and restart   |
| `mockSockets.room()` throws `no room socket has connected yet`  | No room page was opened                                                                | Create or join a room first                                                  |
| A console action throws `MockActionError`                       | The move breaks a room rule (`ROOM_FULL`, `ROLE_CONFLICT`, `NOT_YOUR_TURN`, …)         | Read `error.code`; set up a valid state first, for example with `scenario()` |
| Room shows the Reconnecting overlay forever                     | `mockSockets.offline()` without a duration                                             | Run `mockSockets.online()`                                                   |
| Every REST call fails                                           | A forced outcome or `latencyMs` is still set                                           | `mockApi.config.outcomes = {}` or reload the page                            |
| A room from a previous session is gone                          | Mock state is in memory                                                                | Expected: a reload restores the seed                                         |
| Port 5173 is in use                                             | Another Vite server                                                                    | Use the URL Vite prints, or stop the other server                            |
| Session from the real backend looks stale                       | Old cookies                                                                            | Log out, or clear the `ft_session` and `ft_refresh` cookies for `localhost`  |
