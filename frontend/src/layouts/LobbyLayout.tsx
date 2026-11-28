import { Outlet } from 'react-router-dom';

import { Footer } from './Footer';

/**
 * Authenticated lobby shell. The page draws its own header and content column; the profile
 * modal is mounted at the app level, so this layout only frames
 * the page above the legal footer.
 */
export function LobbyLayout() {
  return (
    <div className="flex min-h-screen flex-col bg-background">
      <Outlet />
      <Footer />
    </div>
  );
}
