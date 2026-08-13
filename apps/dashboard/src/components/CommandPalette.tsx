import { useQuery } from '@tanstack/react-query';
import {
  BadgeCheck,
  Box,
  CircleUser,
  Container,
  FileText,
  Fingerprint,
  GitBranch,
  HardDriveDownload,
  Info,
  Key,
  KeyRound,
  ScrollText,
  Settings,
  ShieldCheck,
  Users,
} from 'lucide-react';
import { useNavigate } from 'react-router';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useSettings } from '@/lib/SettingsContext';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { observeNavigation } from '@/ee/pages/Observe/navigation';
import {
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandSeparator,
} from '@/components/ui/command';

type NavigationItem = {
  label: string;
  path: string;
  icon: typeof Box;
};

export const CommandPalette = ({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) => {
  const navigate = useNavigate();
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { isAdmin } = useCurrentUser();
  const { apps, selectedAppId, setSelectedAppId } = useSelectedApp();

  const channelsQuery = useQuery({
    queryKey: ['channels', selectedAppId],
    queryFn: () => api.getChannels(),
    enabled: open && !!selectedAppId,
  });
  const branchesQuery = useQuery({
    queryKey: ['branches', selectedAppId],
    queryFn: () => api.getBranches(),
    enabled: open && !!selectedAppId,
  });

  const appNavigation: NavigationItem[] = selectedAppId
    ? [
        ...(CONTROL_PLANE_ENABLED
          ? [{ label: 'Updates', path: '/updates', icon: HardDriveDownload }]
          : []),
        // Every Observe page is its own destination, so each is reachable
        // directly: "logs" or "releases" is what people type, not "observe".
        ...(CONTROL_PLANE_ENABLED
          ? observeNavigation.map(page => ({
              label: `Observe · ${page.label}`,
              path: `/observe/${page.value}`,
              icon: page.icon,
            }))
          : []),
        { label: 'Channels', path: '/channels', icon: Box },
        { label: 'Branches', path: '/branches', icon: GitBranch },
        { label: 'App info', path: '/app-info', icon: Info },
        ...(CONTROL_PLANE_ENABLED
          ? [
              { label: 'API tokens', path: '/tokens', icon: KeyRound },
              { label: 'Build credentials', path: '/credentials', icon: Key },
              { label: 'Environment variables', path: '/credentials', icon: Container },
            ]
          : []),
      ]
    : [];
  const serverNavigation: NavigationItem[] = [
    { label: 'Settings', path: '/settings', icon: Settings },
    ...(CONTROL_PLANE_ENABLED ? [{ label: 'License', path: '/license', icon: BadgeCheck }] : []),
    { label: 'My account', path: '/account', icon: CircleUser },
  ];
  const accessSecurityNavigation: NavigationItem[] =
    CONTROL_PLANE_ENABLED && isAdmin
      ? [
          { label: 'Users', path: '/users', icon: Users },
          { label: 'Roles', path: '/roles', icon: ShieldCheck },
          { label: 'SSO', path: '/sso', icon: Fingerprint },
          { label: 'Audit log', path: '/audit-logs', icon: ScrollText },
        ]
      : [];

  const goTo = (path: string) => {
    onOpenChange(false);
    navigate(path);
  };

  return (
    <CommandDialog open={open} onOpenChange={onOpenChange}>
      <CommandInput placeholder="Search pages, branches, channels..." />
      <CommandList className="max-h-[min(420px,60vh)]">
        <CommandEmpty>No result found.</CommandEmpty>

        {appNavigation.length > 0 && (
          <CommandGroup heading="Application">
            {appNavigation.map(item => (
              <CommandItem
                key={item.label}
                value={`page ${item.label}`}
                onSelect={() => goTo(item.path)}>
                <item.icon />
                <span>{item.label}</span>
              </CommandItem>
            ))}
          </CommandGroup>
        )}

        {selectedAppId && (channelsQuery.data?.length ?? 0) > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Channels">
              {channelsQuery.data?.map(channel => (
                <CommandItem
                  key={channel.releaseChannelId}
                  value={`channel ${channel.releaseChannelName}`}
                  onSelect={() =>
                    goTo(`/channels/${encodeURIComponent(channel.releaseChannelName)}`)
                  }>
                  <Box />
                  <span className="truncate">{channel.releaseChannelName}</span>
                  {channel.branchName && (
                    <span className="ml-auto truncate text-xs text-muted-foreground">
                      {channel.branchName}
                    </span>
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {selectedAppId && (branchesQuery.data?.length ?? 0) > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Branches">
              {branchesQuery.data?.map(branch => (
                <CommandItem
                  key={branch.branchId}
                  value={`branch ${branch.branchName}`}
                  onSelect={() => goTo(`/branches/${encodeURIComponent(branch.branchName)}`)}>
                  <GitBranch />
                  <span className="truncate">{branch.branchName}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        <CommandSeparator />
        <CommandGroup heading="Server">
          {serverNavigation.map(item => (
            <CommandItem
              key={item.label}
              value={`page ${item.label}`}
              onSelect={() => goTo(item.path)}>
              <item.icon />
              <span>{item.label}</span>
            </CommandItem>
          ))}
        </CommandGroup>

        {accessSecurityNavigation.length > 0 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Access & Security">
              {accessSecurityNavigation.map(item => (
                <CommandItem
                  key={item.label}
                  value={`page ${item.label}`}
                  onSelect={() => goTo(item.path)}>
                  <item.icon />
                  <span>{item.label}</span>
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}

        {apps.length > 1 && (
          <>
            <CommandSeparator />
            <CommandGroup heading="Applications">
              {apps.map(app => (
                <CommandItem
                  key={app.id}
                  value={`application ${app.name || app.id}`}
                  onSelect={() => {
                    setSelectedAppId(app.id);
                    goTo(CONTROL_PLANE_ENABLED ? '/updates' : '/branches');
                  }}>
                  <FileText />
                  <span className="truncate">{app.name || app.id}</span>
                  {app.id === selectedAppId && (
                    <span className="ml-auto h-1.5 w-1.5 rounded-full bg-primary" />
                  )}
                </CommandItem>
              ))}
            </CommandGroup>
          </>
        )}
      </CommandList>
    </CommandDialog>
  );
};
