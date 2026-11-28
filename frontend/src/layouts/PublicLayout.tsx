import { Outlet } from 'react-router-dom';

import { Footer } from './Footer';
import { Header } from './Header';

/** Unauthenticated shell for public screens without their own header and footer. */
export function PublicLayout() {
  return (
    <div className="flex min-h-screen flex-col">
      <Header />
      <main className="mx-auto flex w-full max-w-7xl flex-1 flex-col px-3 py-4 sm:px-4">
        <Outlet />
      </main>
      <Footer />
    </div>
  );
}
