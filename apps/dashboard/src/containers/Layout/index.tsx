import { AppSidebar } from '@/components/app-sidebar';
import { ExpoImportBanner } from '@/components/expo-import-banner';
import { LicenseGraceBanner } from '@/ee/components/LicenseGraceBanner';
import wordmark from '@/assets/xprem-wordmark.svg';
import wordmarkOnLight from '@/assets/xprem-wordmark-on-light.svg';
import { useEffect, useState } from 'react';
import { Menu } from 'lucide-react';
import { useLocation } from 'react-router';
import { Button } from '@/components/ui/button';
import { Sheet, SheetContent, SheetTitle } from '@/components/ui/sheet';
import { CommandPalette } from '@/components/CommandPalette';

export const Layout = ({ children }: { children: React.ReactNode }) => {
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const [commandPaletteOpen, setCommandPaletteOpen] = useState(false);
  const { pathname } = useLocation();

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.repeat || event.key.toLowerCase() !== 'k' || (!event.metaKey && !event.ctrlKey))
        return;
      event.preventDefault();
      setCommandPaletteOpen(current => !current);
    };
    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, []);

  const openCommandPalette = () => {
    setMobileNavOpen(false);
    setCommandPaletteOpen(true);
  };

  return (
    <>
      <div className="flex min-h-screen w-full bg-background">
        <AppSidebar onOpenCommandPalette={openCommandPalette} />
        <main className="min-w-0 flex-1">
          <LicenseGraceBanner />
          <ExpoImportBanner />
          <header className="sticky top-0 z-30 flex h-14 items-center justify-between border-b border-border/80 bg-background/95 px-4 backdrop-blur lg:hidden">
            {/* -ml-5 cancels the wordmark's internal left padding at this scale. */}
            <div className="-ml-5">
              <img src={wordmarkOnLight} alt="xprem" className="h-8 w-32 object-cover dark:hidden" />
              <img src={wordmark} alt="xprem" className="hidden h-8 w-32 object-cover dark:block" />
            </div>
            <Button
              variant="ghost"
              size="icon"
              aria-label="Open navigation"
              onClick={() => setMobileNavOpen(true)}>
              <Menu className="h-5 w-5" />
            </Button>
          </header>
          <div
            key={pathname}
            className="dashboard-page-enter mx-auto max-w-[1480px] px-4 py-6 sm:px-6 lg:px-10 lg:py-10">
            {children}
          </div>
        </main>
      </div>
      <Sheet open={mobileNavOpen} onOpenChange={setMobileNavOpen}>
        <SheetContent side="left" className="w-72 p-0">
          <SheetTitle className="sr-only">Navigation</SheetTitle>
          <AppSidebar
            mobile
            onNavigate={() => setMobileNavOpen(false)}
            onOpenCommandPalette={openCommandPalette}
          />
        </SheetContent>
      </Sheet>
      <CommandPalette open={commandPaletteOpen} onOpenChange={setCommandPaletteOpen} />
    </>
  );
};
