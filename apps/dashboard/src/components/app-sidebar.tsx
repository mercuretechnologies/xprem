import { useEffect, useId, useState } from 'react';
import { Link, useLocation } from 'react-router';
import {
  BadgeCheck,
  Box,
  ChevronDown,
  ChartNoAxesCombined,
  CircleUser,
  Fingerprint,
  ScrollText,
  HardDriveDownload,
  GitBranch,
  Info,
  KeyRound,
  Lock,
  LogOut,
  Monitor,
  Moon,
  Plus,
  Key,
  Container,
  Rocket,
  Search,
  Server,
  Settings,
  ShieldCheck,
  Sun,
  Users,
  Wrench,
} from 'lucide-react';
import clsx from 'clsx';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { Combobox } from '@/components/Combobox';
import wordmark from '@/assets/xprem-wordmark.svg';
import wordmarkOnLight from '@/assets/xprem-wordmark-on-light.svg';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { CreateAppModal } from '@/components/app-creation';
import { useSettings } from '@/lib/SettingsContext';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { EnterpriseBadge } from '@/ee/components/EnterpriseBadge';
import { observeNavigation } from '@/ee/pages/Observe/navigation';
import { ThemePreference, useTheme } from '@/lib/theme';

const NavLink = ({
  to,
  icon: Icon,
  badge,
  onNavigate,
  children,
}: {
  to: string;
  icon: typeof Box;
  badge?: React.ReactNode;
  onNavigate?: () => void;
  children: React.ReactNode;
}) => {
  const { pathname } = useLocation();
  const isActive = pathname === to || pathname.startsWith(`${to}/`);
  return (
    <Link
      to={to}
      onClick={e => {
        if (pathname === to) e.preventDefault();
        onNavigate?.();
      }}
      className={clsx(
        'flex items-center gap-2.5 rounded-md border border-transparent px-3 py-2 text-sm transition-all duration-150 motion-reduce:transition-none',
        isActive
          ? 'border-primary/20 bg-primary/10 font-medium text-foreground'
          : 'text-muted-foreground hover:translate-x-0.5 hover:border-border hover:bg-accent/70 hover:text-foreground motion-reduce:hover:translate-x-0'
      )}>
      <Icon className="h-4 w-4" strokeWidth={1.75} />
      <span>{children}</span>
      {badge}
    </Link>
  );
};

// Smaller nav entry for links nested under an expandable group.
const SubNavLink = ({
  to,
  icon: Icon,
  badge,
  title,
  onNavigate,
  children,
}: {
  to: string;
  icon: typeof Box;
  badge?: React.ReactNode;
  title?: string;
  onNavigate?: () => void;
  children: React.ReactNode;
}) => {
  const { pathname } = useLocation();
  // `to` may carry a query string (Observe); only the path decides active state.
  const path = to.split('?')[0];
  const isActive = pathname === path || pathname.startsWith(`${path}/`);
  return (
    <Link
      to={to}
      title={title}
      onClick={e => {
        if (pathname === path) e.preventDefault();
        onNavigate?.();
      }}
      className={clsx(
        'flex items-center gap-2.5 rounded-md px-3 py-1.5 text-[13px] transition-colors',
        isActive
          ? 'bg-accent font-medium text-foreground'
          : 'text-muted-foreground hover:bg-accent/60 hover:text-foreground'
      )}>
      <Icon className="h-3.5 w-3.5" strokeWidth={1.75} />
      <span>{children}</span>
      {badge}
    </Link>
  );
};

// Marks a nav entry as part of the Enterprise edition, with the emerald
// accent shared by the enterprise UI.
const EnterpriseNavBadge = () => (
  <span className="ml-auto rounded-full border border-emerald-400/25 bg-emerald-400/10 px-1.5 py-px text-[10px] font-medium text-emerald-700 dark:text-emerald-300">
    Enterprise
  </span>
);

// Counts the accounts waiting for an admin to approve them. Without it nobody
// notices there is anything to approve, and new members sit blocked in silence.
const PendingUsersBadge = ({ count }: { count: number }) => (
  <span
    className="ml-auto rounded-full border border-amber-400/25 bg-amber-400/10 px-1.5 py-px text-[10px] font-medium text-amber-700 dark:text-amber-300"
    title={`${count} account${count > 1 ? 's' : ''} waiting for approval`}>
    {count}
  </span>
);

// Observe is a set of pages, not one page: each answers a different question
// and people go straight to the one they need. They are sub-entries here
// rather than tabs inside the page so the destination is visible before you
// arrive, and so the page keeps its full height for the data.
const ObserveNav = ({ onNavigate }: { onNavigate?: () => void }) => {
  const { pathname, search } = useLocation();
  const isActive = pathname === '/observe' || pathname.startsWith('/observe/');

  // Filters, period and live state all live in the query string. Carrying it
  // across sub-pages is the whole point: you narrow to a branch once, then
  // walk performance, events and logs on that same slice.
  const carried = isActive ? search : '';

  return (
    <ExpandableSection
      label="Observe"
      icon={ChartNoAxesCombined}
      to={`/observe/overview${carried}`}
      paths={['/observe']}
      onNavigate={onNavigate}>
      {observeNavigation.map(page => (
        <SubNavLink
          key={page.value}
          to={`/observe/${page.value}${carried}`}
          icon={page.icon}
          title={page.question}
          onNavigate={onNavigate}>
          {page.label}
        </SubNavLink>
      ))}
    </ExpandableSection>
  );
};

const SectionLabel = ({ children }: { children: React.ReactNode }) => (
  <p className="px-3 pb-1.5 pt-5 text-xs font-medium text-muted-foreground">{children}</p>
);

// Collapsible nav group with the same behavior as Observe: the header is a
// link to the group's first page, the chevron alone toggles the sub-entries.
const ExpandableSection = ({
  label,
  icon: Icon,
  to,
  paths,
  onNavigate,
  children,
}: {
  label: string;
  icon?: typeof Box;
  to: string;
  paths: string[];
  onNavigate?: () => void;
  children: React.ReactNode;
}) => {
  const { pathname } = useLocation();
  const contentId = useId();
  const isActive = paths.some(path => pathname === path || pathname.startsWith(`${path}/`));
  const [isOpen, setIsOpen] = useState(isActive);

  useEffect(() => {
    if (isActive) {
      setIsOpen(true);
    }
  }, [isActive]);

  return (
    <div>
      <div
        className={clsx(
          'flex items-center rounded-md border border-transparent pr-1 transition-all duration-150 motion-reduce:transition-none',
          isActive
            ? 'border-primary/20 bg-primary/10 text-foreground'
            : 'text-muted-foreground hover:border-border hover:bg-accent/70 hover:text-foreground'
        )}>
        <Link
          to={to}
          onClick={e => {
            if (isOpen) {
              e.preventDefault();
              setIsOpen(false);
              return;
            }
            setIsOpen(true);
            onNavigate?.();
          }}
          className={clsx(
            'flex min-w-0 flex-1 items-center gap-2.5 px-3 py-2 text-sm',
            isActive && 'font-medium'
          )}>
          {Icon && <Icon className="h-4 w-4" strokeWidth={1.75} />}
          <span>{label}</span>
        </Link>
        <button
          type="button"
          aria-expanded={isOpen}
          aria-controls={contentId}
          aria-label={isOpen ? `Collapse ${label}` : `Expand ${label}`}
          onClick={() => setIsOpen(open => !open)}
          className="rounded p-1 text-muted-foreground hover:bg-accent hover:text-foreground">
          <ChevronDown
            className={clsx(
              'h-3.5 w-3.5 transition-transform duration-150 motion-reduce:transition-none',
              isOpen && 'rotate-180'
            )}
          />
        </button>
      </div>

      {isOpen && (
        <div id={contentId} className="ml-4 mt-0.5 space-y-0.5 border-l border-border/70 pl-2">
          {children}
        </div>
      )}
    </div>
  );
};

const serverPaths = ['/settings', '/license', '/account'];
const accessSecurityPaths = ['/users', '/roles', '/sso', '/audit-logs'];
const otaPaths = ['/updates', '/channels', '/branches']
const buildPaths = ['/build-credentials', '/environments'];

const themeOptions: Array<{
  value: ThemePreference;
  label: string;
  icon: typeof Sun;
}> = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'system', label: 'Auto', icon: Monitor },
  { value: 'dark', label: 'Dark', icon: Moon },
];

const ThemeSwitcher = () => {
  const { preference, setPreference } = useTheme();
  return (
    <div
      className="grid shrink-0 grid-cols-3 gap-0.5 rounded-md border bg-secondary/70 p-0.5"
      aria-label="Color theme">
      {themeOptions.map(option => {
        const Icon = option.icon;
        const active = preference === option.value;
        return (
          <button
            key={option.value}
            type="button"
            aria-pressed={active}
            aria-label={`${option.label} theme`}
            title={option.value === 'system' ? 'Automatic theme' : `${option.label} theme`}
            onClick={() => setPreference(option.value)}
            className={clsx(
              'flex h-7 w-7 items-center justify-center rounded text-xs font-medium transition-colors',
              active
                ? 'bg-card text-foreground shadow-card'
                : 'text-muted-foreground hover:bg-accent hover:text-foreground'
            )}>
            <Icon className="h-3.5 w-3.5" />
            <span className="sr-only">{option.label}</span>
          </button>
        );
      })}
    </div>
  );
};

export function AppSidebar({
  mobile = false,
  onNavigate,
  onOpenCommandPalette,
}: {
  mobile?: boolean;
  onNavigate?: () => void;
  onOpenCommandPalette?: () => void;
} = {}) {
  const { CONTROL_PLANE_ENABLED, SERVER_VERSION } = useSettings();
  const { isAdmin } = useCurrentUser();
  const { apps, selectedAppId, setSelectedAppId, refreshApps, isLoading } = useSelectedApp();
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);

  // Same query key as the Users page, so react-query serves both from one
  // request and approving an account refreshes the badge on its own.
  const usersQuery = useQuery({
    queryKey: ['users'],
    queryFn: () => api.getUsers(),
    enabled: CONTROL_PLANE_ENABLED && isAdmin,
  });
  const licenseQuery = useQuery({
    queryKey: ['license'],
    queryFn: () => api.getLicense(),
    enabled: CONTROL_PLANE_ENABLED,
  });
  const pendingUsersCount = (usersQuery.data ?? []).filter(user => !user.enabled).length;
  // Anything other than a confirmed valid license shows the badges, including
  // while the query is still in flight or after it failed. Testing for an
  // explicit `false` made them appear a beat late on a community deployment,
  // and vanish entirely when /license was unreachable: a community user would
  // see Roles, SSO and Audit log with nothing saying they are Enterprise.
  const showEnterpriseNavBadges = licenseQuery.data?.valid !== true;
  const commandPaletteShortcut =
    typeof navigator !== 'undefined' && /Mac|iPhone|iPad|iPod/i.test(navigator.userAgent)
      ? '⌘ K'
      : 'Ctrl K';

  const handleAppCreated = async (newAppId: string) => {
    await refreshApps();
    setSelectedAppId(newAppId);
  };

  return (
    <>
      <aside
        className={clsx(
          'h-screen w-64 shrink-0 flex-col border-r border-border/80 bg-card dark:bg-[#09090b]',
          mobile ? 'flex w-full' : 'sticky top-0 hidden lg:flex'
        )}>
        {/* The wordmark svg carries its own padding, so the container has no px:
            the mark's internal margin lands where px-5 used to. */}
        <div className="pb-2 pt-5">
          <img src={wordmarkOnLight} alt="xprem" className="h-8 w-32 object-cover dark:hidden" />
          <img src={wordmark} alt="xprem" className="hidden h-8 w-32 object-cover dark:block" />
        </div>

        <div className="px-3 pt-3">
          {/* Always rendered, even with a single app: the selector is what tells
              you which app every view below is scoped to. Creating apps only
              exists on the control plane and is an admin action, so the action
              is gated on both. */}
          <Combobox
            className="h-10 w-full rounded-lg"
            label="Select app"
            options={apps.map(a => ({ value: a.id, label: a.name || a.id }))}
            value={selectedAppId ?? ''}
            onChange={v => {
              if (v) setSelectedAppId(v);
            }}
            loading={isLoading}
            action={
              CONTROL_PLANE_ENABLED && isAdmin
                ? {
                    label: 'New application',
                    icon: <Plus className="mr-2 h-4 w-4" />,
                    onSelect: () => setIsCreateModalOpen(true),
                  }
                : undefined
            }
          />
          <button
            type="button"
            onClick={onOpenCommandPalette}
            aria-keyshortcuts="Meta+K Control+K"
            className="mt-2 flex h-9 w-full items-center gap-2.5 rounded-md border border-transparent px-3 text-sm text-muted-foreground transition-all duration-150 hover:border-border hover:bg-accent/70 hover:text-foreground">
            <Search className="h-4 w-4" />
            <span>Search</span>
            <kbd className="ml-auto rounded border border-border bg-secondary px-1.5 py-0.5 text-[11px] font-medium text-muted-foreground">
              {commandPaletteShortcut}
            </kbd>
          </button>
        </div>

        <nav className="flex-1 overflow-y-auto px-3">
          {/* App-scoped pages are meaningless without a selected app (fresh
              control-plane install with no app yet), so hide the whole section
              until one is selected. */}
          {selectedAppId && (
            <>
              <SectionLabel>Application</SectionLabel>
              <div className="space-y-0.5">
                <ExpandableSection
                  label="OTA Updates"
                  icon={Rocket}
                  to={CONTROL_PLANE_ENABLED ? '/updates' : '/channels'}
                  paths={otaPaths}
                  onNavigate={onNavigate}>
                  {CONTROL_PLANE_ENABLED && (
                    <SubNavLink to="/updates" icon={HardDriveDownload} onNavigate={onNavigate}>
                      Updates
                    </SubNavLink>
                  )}
                  <SubNavLink to="/channels" icon={Box} onNavigate={onNavigate}>
                    Channels
                  </SubNavLink>
                  <SubNavLink to="/branches" icon={GitBranch} onNavigate={onNavigate}>
                    Branches
                  </SubNavLink>
                </ExpandableSection>
                {CONTROL_PLANE_ENABLED && <ObserveNav onNavigate={onNavigate} />}
                
                {CONTROL_PLANE_ENABLED && (
                  <>
                    <ExpandableSection
                      label="Builds"
                      icon={Wrench}
                      to={'/build-credentials'}
                      paths={buildPaths}
                      onNavigate={onNavigate}>
                        <SubNavLink to="/build-credentials" icon={Key} onNavigate={onNavigate}>
                          Credentials
                        </SubNavLink>
                        <SubNavLink to="/environments" icon={Container} onNavigate={onNavigate}>
                          Environments
                        </SubNavLink>
                    </ExpandableSection>
                    <NavLink to="/tokens" icon={KeyRound} onNavigate={onNavigate}>
                      API tokens
                    </NavLink>
                  </>
                )}
                <NavLink to="/app-info" icon={Info} onNavigate={onNavigate}>
                  App info
                </NavLink>
              </div>

              <div className="mx-3 mt-5 border-t border-border/70" />
            </>
          )}

          <div className="mt-3 space-y-0.5">
            <ExpandableSection
              label="Server"
              icon={Server}
              to="/settings"
              paths={serverPaths}
              onNavigate={onNavigate}>
              <SubNavLink to="/settings" icon={Settings} onNavigate={onNavigate}>
                Settings
              </SubNavLink>
              {CONTROL_PLANE_ENABLED && (
                <SubNavLink to="/license" icon={BadgeCheck} onNavigate={onNavigate}>
                  License
                </SubNavLink>
              )}
              <SubNavLink to="/account" icon={CircleUser} onNavigate={onNavigate}>
                My account
              </SubNavLink>
            </ExpandableSection>

            {/* Who signs in and how: accounts on one side, SSO on the other.
                Both are control-plane, admin-managed concerns. */}
            {CONTROL_PLANE_ENABLED && isAdmin && (
              <ExpandableSection
                label="Access & Security"
                icon={Lock}
                to="/users"
                paths={accessSecurityPaths}
                onNavigate={onNavigate}>
                <SubNavLink
                  to="/users"
                  icon={Users}
                  onNavigate={onNavigate}
                  badge={
                    pendingUsersCount > 0 ? (
                      <PendingUsersBadge count={pendingUsersCount} />
                    ) : undefined
                  }>
                  Users
                </SubNavLink>
                <SubNavLink
                  to="/roles"
                  icon={ShieldCheck}
                  badge={showEnterpriseNavBadges ? <EnterpriseNavBadge /> : undefined}
                  onNavigate={onNavigate}>
                  Roles
                </SubNavLink>
                <SubNavLink
                  to="/sso"
                  icon={Fingerprint}
                  badge={showEnterpriseNavBadges ? <EnterpriseNavBadge /> : undefined}
                  onNavigate={onNavigate}>
                  SSO
                </SubNavLink>
                <SubNavLink
                  to="/audit-logs"
                  icon={ScrollText}
                  badge={showEnterpriseNavBadges ? <EnterpriseNavBadge /> : undefined}
                  onNavigate={onNavigate}>
                  Audit log
                </SubNavLink>
              </ExpandableSection>
            )}
          </div>
        </nav>

        <div className="border-t border-border/80">
          <EnterpriseBadge />

          <div className="flex items-center gap-2 p-3">
            <Link
              to="/logout"
              onClick={onNavigate}
              className="flex min-w-0 flex-1 items-center gap-2.5 rounded-md border border-transparent px-2 py-1.5 text-sm text-muted-foreground transition-colors hover:border-border hover:bg-accent/70 hover:text-foreground">
              <LogOut className="h-4 w-4" strokeWidth={1.75} />
              <span>Log out</span>
            </Link>
            <ThemeSwitcher />
          </div>

          <p className="px-3 pb-3 text-center font-mono text-[10px] text-muted-foreground/70">
            Server {SERVER_VERSION}
          </p>
        </div>
      </aside>

      {CONTROL_PLANE_ENABLED && isAdmin && (
        <CreateAppModal
          isOpen={isCreateModalOpen}
          onClose={() => setIsCreateModalOpen(false)}
          onAppCreated={handleAppCreated}
        />
      )}
    </>
  );
}
