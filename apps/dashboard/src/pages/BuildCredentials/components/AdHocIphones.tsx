import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Check, Copy, Link2Off, Plus, RefreshCw } from 'lucide-react';
import { ApiError } from '@/components/APIError';
import { api, AppleDevice, describeApiError, IosDeviceInvitation } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { cn, formatTimestamp } from '@/lib/utils';
import { useToast } from '@/hooks/use-toast';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { deviceModelName } from '../iosDeviceModels';
import { InviteIphonesDialog } from './InviteIphonesDialog';

/** Abbreviates long device identifiers for display while retaining both ends. */
const shortUdid = (udid: string) =>
  udid.length > 12 ? `${udid.slice(0, 8)}…${udid.slice(-4)}` : udid;

/** Apple may send a date the server cannot parse; it is then shown as received. */
const formatDate = (date: string) => formatTimestamp(date) ?? date;

/** Copies the complete device identifier and reports clipboard failures. */
const CopyUdidButton = ({ udid }: { udid: string }) => {
  const { toast } = useToast();
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      title="Copy UDID"
      aria-label="Copy UDID"
      className="text-muted-foreground hover:text-foreground"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(udid);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          toast({ title: 'Could not copy', description: udid, variant: 'destructive' });
        }
      }}>
      {copied ? (
        <Check className="h-3.5 w-3.5 text-emerald-700 dark:text-emerald-300" />
      ) : (
        <Copy className="h-3.5 w-3.5" />
      )}
    </button>
  );
};

/** Displays a device status, registration source and available management actions. */
const DeviceRow = ({
  device,
  canManage,
  isBusy,
  isUpdating,
  onRevoke,
  onEnable,
}: {
  device: AppleDevice;
  canManage: boolean;
  isBusy: boolean;
  isUpdating: boolean;
  onRevoke: () => void;
  onEnable: () => void;
}) => {
  const enabled = device.status === 'ENABLED';
  const origin = device.registeredVia
    ? `Added ${formatDate(device.registeredVia.registeredAt)} via ${device.registeredVia.label || 'a registration link'}`
    : `Added ${formatDate(device.addedAt)} outside xprem`;
  return (
    <li className="flex flex-wrap items-start justify-between gap-3 px-4 py-3">
      <div className="min-w-0 space-y-1">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="font-medium">{device.name}</span>
          <span className="text-muted-foreground">{deviceModelName(device)}</span>
          <Badge
            variant="outline"
            className={
              enabled
                ? 'border-emerald-400/25 bg-emerald-400/10 text-emerald-700 dark:text-emerald-300'
                : 'border-border bg-muted text-muted-foreground'
            }>
            {enabled ? 'Enabled' : 'Disabled'}
          </Badge>
        </div>
        <p className="flex flex-wrap items-center gap-x-1.5 text-xs text-muted-foreground">
          {device.osVersion && <span>iOS {device.osVersion} ·</span>}
          <span>{origin} ·</span>
          <span className="font-mono">{shortUdid(device.udid)}</span>
          <CopyUdidButton udid={device.udid} />
        </p>
      </div>
      {canManage &&
        (enabled ? (
          <Button
            variant="ghost"
            size="sm"
            onClick={onRevoke}
            disabled={isBusy}
            className="h-8 text-muted-foreground hover:bg-destructive/10 hover:text-destructive">
            Revoke
          </Button>
        ) : (
          <Button variant="outline" size="sm" onClick={onEnable} disabled={isBusy}>
            {isUpdating ? 'Enabling…' : 'Enable'}
          </Button>
        ))}
    </li>
  );
};

// The devices list calls Apple and needs credentials:manage, so viewers only see pending links.
/** Lists Apple devices and manages single-use registration invitations. */
export const AdHocIphones = ({ canManage }: { canManage: boolean }) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const [isInviteDialogOpen, setIsInviteDialogOpen] = useState(false);
  const [deviceToRevoke, setDeviceToRevoke] = useState<AppleDevice | null>(null);
  // The device being disabled or enabled at Apple; every device action waits for it.
  const [updatingDeviceId, setUpdatingDeviceId] = useState<string | null>(null);
  const [revokingInvitationId, setRevokingInvitationId] = useState<string | null>(null);

  const devicesQuery = useQuery({
    queryKey: ['iosDevices', selectedAppId],
    queryFn: () => api.getIosDevices(),
    enabled: !!selectedAppId && canManage,
  });

  const invitationsQuery = useQuery({
    queryKey: ['iosDeviceInvitations', selectedAppId],
    queryFn: () => api.getIosDeviceInvitations(),
    enabled: !!selectedAppId,
  });

  /** Refreshes invitation lifecycle states after creation or revocation. */
  const invalidateInvitations = () =>
    queryClient.invalidateQueries({ queryKey: ['iosDeviceInvitations', selectedAppId] });

  /** Changes the Apple device status and refreshes its cached entry. */
  const updateDevice = async (device: AppleDevice, action: 'disable' | 'enable') => {
    setUpdatingDeviceId(device.id);
    try {
      if (action === 'disable') {
        await api.disableIosDevice(device.id);
      } else {
        await api.enableIosDevice(device.id);
      }
      await queryClient.invalidateQueries({ queryKey: ['iosDevices', selectedAppId] });
      toast({
        title: action === 'disable' ? 'iPhone revoked' : 'iPhone enabled',
        description: device.name,
      });
      setDeviceToRevoke(null);
    } catch (error) {
      const message = describeApiError(
        error,
        action === 'disable' ? 'Could not revoke iPhone' : 'Could not enable iPhone'
      );
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setUpdatingDeviceId(null);
    }
  };

  /** Revokes a registration invitation and refreshes the invitation list. */
  const handleRevokeInvitation = async (invitation: IosDeviceInvitation) => {
    setRevokingInvitationId(invitation.id);
    try {
      await api.revokeIosDeviceInvitation(invitation.id);
      await invalidateInvitations();
      toast({ title: 'Invitation revoked', description: 'The link no longer works.' });
    } catch (error) {
      const message = describeApiError(error, 'Could not revoke invitation');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setRevokingInvitationId(null);
    }
  };

  const devices = devicesQuery.data ?? [];
  const pendingInvitations = (invitationsQuery.data ?? []).filter(
    invitation => invitation.status === 'pending'
  );
  const isRefreshing = devicesQuery.isFetching || invitationsQuery.isFetching;

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-sm font-medium">
          {canManage &&
            devicesQuery.isSuccess &&
            `${devices.length} ${devices.length === 1 ? 'iPhone' : 'iPhones'}`}
        </span>
        <div className="flex gap-2">
          <Button
            variant="outline"
            size="sm"
            disabled={isRefreshing}
            onClick={() => {
              if (canManage) void devicesQuery.refetch();
              void invitationsQuery.refetch();
            }}>
            <RefreshCw className={cn('h-3.5 w-3.5', isRefreshing && 'animate-spin')} /> Refresh
          </Button>
          {canManage && (
            <Button size="sm" variant="outline" onClick={() => setIsInviteDialogOpen(true)}>
              <Plus className="h-3.5 w-3.5" /> Invite an iPhone
            </Button>
          )}
        </div>
      </div>

      {!canManage && (
        <p className="text-sm text-muted-foreground">
          Only people who can manage build credentials see the registered iPhones.
        </p>
      )}

      {canManage &&
        (devicesQuery.isPending ? (
          <Skeleton className="h-16 w-full rounded-lg" />
        ) : devicesQuery.isError ? (
          <ApiError error={devicesQuery.error} onRetry={() => void devicesQuery.refetch()} />
        ) : devices.length === 0 ? (
          <p className="text-sm text-muted-foreground">No iPhone registered yet.</p>
        ) : (
          <ul className="divide-y rounded-lg border">
            {devices.map(device => (
              <DeviceRow
                key={device.id}
                device={device}
                canManage={canManage}
                isBusy={updatingDeviceId !== null}
                isUpdating={updatingDeviceId === device.id}
                onRevoke={() => setDeviceToRevoke(device)}
                onEnable={() => void updateDevice(device, 'enable')}
              />
            ))}
          </ul>
        ))}

      {invitationsQuery.isError && (
        <ApiError error={invitationsQuery.error} onRetry={() => void invitationsQuery.refetch()} />
      )}

      {pendingInvitations.length > 0 && (
        <div className="space-y-2">
          <h4 className="text-sm font-medium">Pending invitations</h4>
          <ul className="divide-y rounded-lg border">
            {pendingInvitations.map(invitation => (
              <li
                key={invitation.id}
                className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                <div className="min-w-0">
                  <p className="truncate text-sm">{invitation.label || 'Unnamed invitation'}</p>
                  <p className="text-xs text-muted-foreground">
                    Expires {formatDate(invitation.expiresAt)}
                  </p>
                </div>
                {canManage && (
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={revokingInvitationId === invitation.id}
                    onClick={() => void handleRevokeInvitation(invitation)}
                    className="h-8 text-muted-foreground hover:bg-destructive/10 hover:text-destructive">
                    <Link2Off className="h-3.5 w-3.5" />
                    {revokingInvitationId === invitation.id ? 'Revoking…' : 'Revoke'}
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}

      {canManage && (
        <>
          <InviteIphonesDialog
            isOpen={isInviteDialogOpen}
            onClose={() => setIsInviteDialogOpen(false)}
            onCreated={() => void invalidateInvitations()}
          />

          <Dialog
            open={!!deviceToRevoke}
            onOpenChange={open => !open && updatingDeviceId === null && setDeviceToRevoke(null)}>
            <DialogContent className="sm:max-w-[460px]">
              <DialogHeader>
                <DialogTitle>Revoke {deviceToRevoke?.name}?</DialogTitle>
                <DialogDescription className="pt-2">
                  Disables this iPhone at Apple. New Ad Hoc builds won't install on it. Builds
                  already installed keep working until their profile expires. Apple only frees the
                  slot when your membership renews.
                </DialogDescription>
              </DialogHeader>
              <DialogFooter className="border-t pt-4">
                <Button
                  variant="outline"
                  onClick={() => setDeviceToRevoke(null)}
                  disabled={updatingDeviceId !== null}>
                  Cancel
                </Button>
                <Button
                  variant="destructive"
                  onClick={() => deviceToRevoke && void updateDevice(deviceToRevoke, 'disable')}
                  disabled={updatingDeviceId !== null}>
                  {updatingDeviceId !== null ? 'Revoking…' : 'Revoke iPhone'}
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
        </>
      )}
    </div>
  );
};
