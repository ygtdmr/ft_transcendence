# ft_transcendence — Frontend

React SPA for Codenames Online, with feature flows, API clients, domain stores, design tokens and REST/WebSocket mocks. Source and mock behavior do not establish real backend integration or module completion. Feature work and integration are tracked in GitHub Issues.

## Stack

| Area      | Choice                                            |
| --------- | ------------------------------------------------- |
| Framework | React                                             |
| Structure | Feature-based                                     |
| State     | Zustand (domain stores, never one global store)   |
| Routing   | React Router                                      |
| Auth      | Cookie-only session (`ft_session` / `ft_refresh`) |
| Styling   | Tailwind CSS, themed from the design tokens       |
| Icons     | Font Awesome (SVG, tree-shaken per icon)          |
| REST      | Browser `fetch`, no HTTP client dependency        |

The runtime dependencies are React, React Router, Zustand, Tailwind CSS, i18next and
Font Awesome. The frontend uses no HTTP client library, no additional state library and
no component library. Adding one is a team-level choice, not an implementation detail.

## Styling

`src/shared/styles/index.css` is the only stylesheet the application imports. It declares
the cascade layers, pulls in Tailwind's theme, preflight and utilities, and then the
project's own three files: `theme.css` (the design tokens), `base.css` (document
defaults) and `utilities.css` (the few patterns a utility class cannot express).

Every design value comes from the design system in the `ui-design` branch and lives in one
`@theme` block, so Tailwind generates its utilities from the tokens: `bg-primary`,
`text-text-muted`, `rounded-md`, `text-2xl`, `shadow-card`, `p-3`. Components style
themselves with those utilities and never write a raw color, radius, spacing or shadow
value. `src/shared/styles/README.md` documents the token names and the few values that
stay plain custom properties.

Icons are Font Awesome SVGs behind one component, `shared/ui/Icon`. A screen passes a name
from the project icon set (`<Icon name="players" />`); the icon inherits the surrounding font
size and color, so `text-lg` and `text-primary` style it like text. `shared/ui/Icon/icons.ts`
is the full set — a screen that needs a new icon adds it there first, which keeps the
bundle to the icons actually used.

## Layout

```text
src/
├── app/         router, providers, bootstrap, app-level mounts, connection owners
├── layouts/     PublicLayout, LobbyLayout, GameLayout, Footer
├── features/    auth, room, game, profile, friends, stats
├── shared/      api, websocket, stores, types, ui, constants, utils, styles, hooks
└── assets/      icons, fonts, images
```

Two folder names are worth calling out: `shared/constants` holds configuration (API and
WebSocket paths, environment, dev flags), and `shared/utils` holds generic helpers.

### Dependency direction

```text
app  ->  layouts  ->  features  ->  shared
```

- `shared/` never imports `app/`, `layouts/` or `features/`.
- A feature never imports another feature. Cross-feature needs go through `shared/` or
  an app-level mount point (the modal host, the connection providers).
- UI components never call REST or WebSocket directly — always through a feature hook.

The first two rules are enforced by ESLint (`no-restricted-imports`); the third is a
review rule.

## Contract sources

- REST and WebSocket contracts: the `bruno` branch (`collections/`). Rooms and
  games use the Game v2 contract as a pair: REST at `/api/v2/rooms` and the room socket
  at `/ws/v2/rooms/{room_id}`. Identity, profile, friends and match history use v1 under
  `/api/v1`. Social and profile events arrive on the notification socket at
  `/ws/v2/notifications`.
- Visual reference: the `ui-design` branch. Mockups are a visual reference for layout,
  color and typography. They are not a behavioral contract; where a mockup and the Bruno
  contract disagree, the contract wins.

Types in `shared/types` mirror Bruno field names exactly, snake_case included. Do not
invent fields; if the frontend needs something the contract lacks, get the contract
updated first.

## Non-negotiables

- Authentication is cookie-only. Never read, store or attach a session token; the
  session cookie is the only credential, on REST and on both sockets.
- The backend is authoritative for every game rule. The frontend never computes a
  winner — only `game.ended` or a snapshot of the completed game is terminal, and a
  cancelled game has no winner.
- Room and Game share one WebSocket, owned by `app/connection/RoomConnectionProvider`.
  Neither feature opens its own socket.
- Reconnect recovery is a fresh `room.state` snapshot, never an event replay.
- Zero console errors or warnings in the supported evaluation flow.

## Development

Setup, development modes, environment variables, the REST and socket mocks, seeded
test data and manual test recipes are in the
[Frontend Developer Guide](frontend/README.md).

```bash
cd frontend
npm install
npm run dev
```
