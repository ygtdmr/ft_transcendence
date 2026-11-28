import { Navigate, Outlet, RouterProvider, createBrowserRouter } from 'react-router-dom';

import { ModalHost } from '@app/modal/ModalHost';
import { AppToasts } from '@app/notices/AppToasts';
import { NotificationConnectionProvider } from '@app/connection/NotificationConnectionProvider';
import { RoomConnectionProvider } from '@app/connection/RoomConnectionProvider';
import { DisconnectedNotice } from '@features/room/components/ConnectionOverlay';
import { GameLayout } from '@layouts/GameLayout';
import { LobbyLayout } from '@layouts/LobbyLayout';
import { PublicLayout } from '@layouts/PublicLayout';
import { ROUTES } from '@shared/constants';

import { UIElements } from '@app/pages/ui';

import { LandingPage } from '@app/pages/LandingPage';
import { LobbyPage } from '@app/pages/LobbyPage';
import { PrivacyPage } from '@app/pages/PrivacyPage';
import { RoomPage } from '@app/pages/RoomPage';
import { TermsPage } from '@app/pages/TermsPage';

import { RequireAnonymous } from './RequireAnonymous';
import { RequireAuth } from './RequireAuth';
import { LobbyRouteGuard, RoomRouteGuard } from './RouteRecovery';

/** App-level mounts: available from every layout, imported by no feature. */
function AppShell() {
  return (
    <>
      <Outlet />
      <ModalHost />
      <NotificationConnectionProvider />
      <DisconnectedNotice />
      <AppToasts />
    </>
  );
}

/**
 * Route table.
 *
 * Public: landing, login, register, privacy, terms. Authenticated: lobby, room, both held
 * to the server's room membership by the route-recovery guards. Landing and the two legal
 * pages render outside `PublicLayout` because they draw their own header and footer. Login and register are the Landing page with its Log In / Sign Up dialog open.
 * Profile is a modal rendered by `ModalHost`, not a route — so it never changes the
 * underlying screen and stays reachable from Lobby, Room and Game alike.
 *
 * It is a data router so the room can hold a navigation away from it until the player
 * confirms leaving (`useBlocker`).
 */
const router = createBrowserRouter([
  {
    element: <AppShell />,
    children: [
      /*
        Log In and Sign Up are a dialog over the Landing page, opened from its calls to
        action without a route change. `/login` and `/register` link to the same page
        with the dialog open; an authenticated visitor is sent on to the Lobby instead.
      */
      { path: ROUTES.landing, element: <LandingPage /> },
      {
        element: <RequireAnonymous />,
        children: [
          { path: ROUTES.login, element: <LandingPage key="login" authMode="login" /> },
          {
            path: ROUTES.register,
            element: <LandingPage key="register" authMode="register" />,
          },
        ],
      },
      { path: ROUTES.privacy, element: <PrivacyPage /> },
      { path: ROUTES.terms, element: <TermsPage /> },

      // The primitive gallery is a development tool, absent from production builds.
      ...(import.meta.env.DEV
        ? [{ element: <PublicLayout />, children: [{ path: '/ui', element: <UIElements /> }] }]
        : []),

      {
        element: <RequireAuth />,
        children: [
          /*
            Route recovery holds both routes to the server's room membership: a member is
            sent into their room, anyone else to the Lobby.
          */
          {
            element: <LobbyRouteGuard />,
            children: [
              {
                element: <LobbyLayout />,
                children: [{ path: ROUTES.lobby, element: <LobbyPage /> }],
              },
            ],
          },
          /*
            Room and Game share one authoritative WebSocket, so the provider that owns
            it wraps the whole room route rather than living inside either feature.
          */
          {
            path: ROUTES.room,
            element: <RoomRouteGuard />,
            children: [
              {
                element: <RoomConnectionProvider />,
                children: [
                  { element: <GameLayout />, children: [{ index: true, element: <RoomPage /> }] },
                ],
              },
            ],
          },
        ],
      },

      { path: '*', element: <Navigate to={ROUTES.landing} replace /> },
    ],
  },
]);

export function AppRouter() {
  return <RouterProvider router={router} />;
}
