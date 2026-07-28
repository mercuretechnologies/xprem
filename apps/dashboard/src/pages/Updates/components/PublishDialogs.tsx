import { useEffect, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, Undo2 } from 'lucide-react';
import { api, describeApiError } from '@/lib/api';
import { cn } from '@/lib/utils';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/Combobox';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

// Every listing that can name the update at the head of a branch. A republish
// and a rollback both create a new update row, so they stale the same set the
// rollout controls do.
const useInvalidatePublishedUpdates = () => {
  const { selectedAppId } = useSelectedApp();
  const queryClient = useQueryClient();
  return (branch: string, runtimeVersion: string) => {
    queryClient.invalidateQueries({ queryKey: ['updates', selectedAppId, branch, runtimeVersion] });
    queryClient.invalidateQueries({ queryKey: ['update-feed', selectedAppId] });
    queryClient.invalidateQueries({ queryKey: ['runtimeVersions', selectedAppId, branch] });
    queryClient.invalidateQueries({ queryKey: ['update-details', selectedAppId] });
    queryClient.invalidateQueries({ queryKey: ['branches', selectedAppId] });
    queryClient.invalidateQueries({ queryKey: ['channels', selectedAppId] });
  };
};

const platformOptions = [
  { value: '', label: 'Every platform' },
  { value: 'ios', label: 'iOS' },
  { value: 'android', label: 'Android' },
];

// Rolls a branch and runtime version out of OTA: the server answers the next
// manifest request with the rollBackToEmbedded directive, so devices run the
// bundle shipped in the binary again. Going back to an EARLIER update is the
// other action, RepublishDialog, not this one.
//
// Branch and runtime version are picked here rather than inherited from a row:
// a rollback targets the pair, not one update, and during an incident the pair
// is what the operator knows.
export const RollbackDialog = ({
  open,
  onClose,
  defaultBranch = '',
  defaultRuntimeVersion = '',
}: {
  open: boolean;
  onClose: () => void;
  defaultBranch?: string;
  defaultRuntimeVersion?: string;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const invalidate = useInvalidatePublishedUpdates();

  const [branch, setBranch] = useState(defaultBranch);
  const [runtimeVersion, setRuntimeVersion] = useState(defaultRuntimeVersion);
  const [platform, setPlatform] = useState('');
  const [message, setMessage] = useState('');
  const [isBusy, setIsBusy] = useState(false);

  // Reopening the dialog after the feed filters moved should start from where
  // the operator is looking now, not from the last run.
  useEffect(() => {
    if (!open) return;
    setBranch(defaultBranch);
    setRuntimeVersion(defaultRuntimeVersion);
    setPlatform('');
    setMessage('');
  }, [open, defaultBranch, defaultRuntimeVersion]);

  const branchesQuery = useQuery({
    queryKey: ['branches', selectedAppId],
    queryFn: () => api.getBranches(),
    enabled: !!selectedAppId && open,
  });
  const runtimeVersionsQuery = useQuery({
    queryKey: ['runtimeVersions', selectedAppId, branch],
    queryFn: () => api.getRuntimeVersions(branch),
    enabled: !!selectedAppId && open && !!branch,
  });

  const branchOptions = (branchesQuery.data ?? [])
    .map(item => ({ value: item.branchName, label: item.branchName }))
    .sort((a, b) => a.label.localeCompare(b.label));
  const runtimeVersionOptions = (runtimeVersionsQuery.data ?? [])
    .map(item => ({ value: item.runtimeVersion, label: item.runtimeVersion }))
    .sort((a, b) => a.label.localeCompare(b.label));

  const canSubmit = !!branch && !!runtimeVersion && !!message.trim() && !isBusy;

  const handleSubmit = async () => {
    if (!canSubmit) return;
    setIsBusy(true);
    try {
      const result = await api.createRollback(branch, runtimeVersion, {
        message: message.trim(),
        platform: platform || undefined,
      });
      toast({
        title: 'Rollback published',
        description: `${branch} · ${runtimeVersion} falls back to the embedded bundle on ${result.updates.length === 1 ? '1 platform' : `${result.updates.length} platforms`} at the next update check.`,
      });
      invalidate(branch, runtimeVersion);
      onClose();
    } catch (error) {
      const { title, description } = describeApiError(error, 'Could not create the rollback');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsBusy(false);
    }
  };

  return (
    <Dialog open={open} onOpenChange={next => !next && !isBusy && onClose()}>
      <DialogContent className="sm:max-w-[460px]">
        <DialogHeader className="flex flex-col items-start gap-2">
          <div className="flex h-9 w-9 items-center justify-center rounded-full border border-destructive/20 bg-destructive/10 text-destructive">
            <AlertTriangle className="h-5 w-5" />
          </div>
          <DialogTitle className="mt-2 text-lg font-semibold tracking-tight">
            Create a rollback
          </DialogTitle>
          <DialogDescription className="pt-1 text-left text-muted-foreground">
            Devices on this branch and runtime version stop running OTA updates entirely and fall
            back to the bundle shipped inside the app binary, at their next update check. To send
            them to an earlier update instead of out of OTA, republish that update from the list.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-4 py-2">
          <div className="space-y-1.5">
            <Label className="text-xs font-medium text-foreground">Branch</Label>
            <Combobox
              className="w-full"
              label="Select a branch"
              loading={branchesQuery.isLoading}
              disabled={isBusy}
              options={branchOptions}
              value={branch}
              onChange={value => {
                setBranch(value);
                if (value !== branch) setRuntimeVersion('');
              }}
            />
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs font-medium text-foreground">Runtime version</Label>
            <Combobox
              className="w-full"
              label={branch ? 'Select a runtime version' : 'Select a branch first'}
              loading={runtimeVersionsQuery.isLoading}
              disabled={isBusy || !branch}
              options={runtimeVersionOptions}
              value={runtimeVersion}
              onChange={setRuntimeVersion}
            />
          </div>
          <div className="space-y-1.5">
            <Label className="text-xs font-medium text-foreground">Platform</Label>
            <div className="grid grid-cols-3 gap-1 rounded-md border p-1">
              {platformOptions.map(option => (
                <button
                  key={option.value || 'all'}
                  type="button"
                  disabled={isBusy}
                  aria-pressed={platform === option.value}
                  onClick={() => setPlatform(option.value)}
                  className={cn(
                    'rounded-sm px-2 py-1.5 text-xs font-medium transition-colors',
                    platform === option.value
                      ? 'bg-secondary text-foreground'
                      : 'text-muted-foreground hover:text-foreground'
                  )}>
                  {option.label}
                </button>
              ))}
            </div>
            <p className="text-xs text-muted-foreground">
              One rollback per platform, like running the CLI once for each.
            </p>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="rollback-message" className="text-xs font-medium text-foreground">
              Reason
            </Label>
            <Input
              id="rollback-message"
              placeholder="e.g. crash on launch, iOS 18 only"
              value={message}
              disabled={isBusy}
              onChange={event => setMessage(event.target.value)}
            />
            <p className="text-xs text-muted-foreground">
              Shown on the rollback in the updates list, and recorded in the audit log.
            </p>
          </div>
        </div>

        <DialogFooter className="mt-2 gap-2 border-t pt-3 sm:gap-0">
          <Button type="button" variant="outline" onClick={onClose} disabled={isBusy}>
            Cancel
          </Button>
          <Button type="button" variant="destructive" onClick={handleSubmit} disabled={!canSubmit}>
            {isBusy ? 'Rolling back…' : 'Create rollback'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};

// What a republish acts on: one update, or every per-platform member of one
// publish group. `label` is how the feed row names it, so the confirmation
// repeats back exactly what was clicked.
export type RepublishTarget = {
  branch: string;
  runtimeVersion: string;
  label: string;
  platforms: string[];
} & ({ updateId: string; publishGroup?: never } | { publishGroup: string; updateId?: never });

export const RepublishDialog = ({
  target,
  onClose,
}: {
  target: RepublishTarget | null;
  onClose: () => void;
}) => {
  const { toast } = useToast();
  const invalidate = useInvalidatePublishedUpdates();
  const [isBusy, setIsBusy] = useState(false);

  const handleSubmit = async () => {
    if (!target || isBusy) return;
    setIsBusy(true);
    try {
      const result = await api.republishUpdate(
        target.branch,
        target.runtimeVersion,
        target.publishGroup
          ? { publishGroup: target.publishGroup }
          : { updateId: target.updateId as string }
      );
      toast({
        title: 'Update republished',
        description: `${result.updates.length === 1 ? 'One update' : `${result.updates.length} updates`} published to ${target.branch} · ${target.runtimeVersion}. Devices pick it up at their next update check.`,
      });
      invalidate(target.branch, target.runtimeVersion);
      onClose();
    } catch (error) {
      const { title, description } = describeApiError(error, 'Could not republish the update');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsBusy(false);
    }
  };

  return (
    <Dialog open={!!target} onOpenChange={next => !next && !isBusy && onClose()}>
      <DialogContent className="sm:max-w-[440px]">
        <DialogHeader className="flex flex-col items-start gap-2">
          <div className="flex h-9 w-9 items-center justify-center rounded-full border border-primary/20 bg-primary/10 text-link">
            <Undo2 className="h-5 w-5" />
          </div>
          <DialogTitle className="mt-2 text-lg font-semibold tracking-tight">
            Republish this update?
          </DialogTitle>
          <DialogDescription className="pt-1 text-left text-muted-foreground">
            The same bundle is published again as a new update, so it becomes what{' '}
            <span className="font-medium text-foreground">{target?.branch}</span> serves on{' '}
            <span className="font-medium text-foreground">{target?.runtimeVersion}</span>. Nothing
            is deleted: the updates published since stay in the history.
          </DialogDescription>
        </DialogHeader>

        {/* min-w-0: DialogContent is a grid, and a grid item sizes to its
            content by default, so the truncate below never fires and a long
            publish message widens the dialog past its own max-width. */}
        {target && (
          <div className="min-w-0 rounded-md border bg-secondary/40 p-3 text-xs">
            <p className="truncate font-medium text-foreground" title={target.label}>
              {target.label}
            </p>
            <p className="mt-1 text-muted-foreground">
              {target.publishGroup
                ? `Publish group · ${target.platforms.join(', ')}`
                : target.platforms.join(', ')}
            </p>
          </div>
        )}

        <DialogFooter className="mt-2 gap-2 border-t pt-3 sm:gap-0">
          <Button type="button" variant="outline" onClick={onClose} disabled={isBusy}>
            Cancel
          </Button>
          <Button type="button" onClick={handleSubmit} disabled={isBusy}>
            {isBusy ? 'Republishing…' : 'Republish'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
};
