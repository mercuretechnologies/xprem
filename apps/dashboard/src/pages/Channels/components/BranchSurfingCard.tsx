import { useEffect, useState } from 'react';
import { Compass } from 'lucide-react';
import { api, ChannelRecord, describeApiError } from '@/lib/api';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';

const ALL_BRANCHES = '*';

export const BranchSurfingCard = ({
  channel,
  canManage,
  onUpdated,
}: {
  channel: ChannelRecord;
  canManage: boolean;
  onUpdated: () => Promise<void>;
}) => {
  const { toast } = useToast();
  const surfing = channel.branchSurfing;
  const [pattern, setPattern] = useState(surfing?.pattern ?? '');
  const [saving, setSaving] = useState(false);

  // Reseed when the channel is refetched or the route moves to another channel,
  // so the field never shows a stale channel's pattern.
  useEffect(() => {
    setPattern(surfing?.pattern ?? '');
  }, [surfing?.pattern, channel.releaseChannelName]);

  if (!surfing) return null;

  const patternChanged = pattern.trim() !== surfing.pattern;
  // Nothing can be turned on until the pattern says what it opens: defaulting it
  // would expose every branch of the app on the first click.
  const canEnable = pattern.trim().length > 0;

  // The endpoint replaces the whole setting, so both fields go on every call:
  // sending only the switch would reset the pattern to its default.
  const save = async (enabled: boolean, nextPattern: string) => {
    const trimmed = nextPattern.trim();
    if (!trimmed) {
      toast({
        title: 'Pattern is empty',
        description: `Use "${ALL_BRANCHES}" to expose every branch.`,
        variant: 'destructive',
      });
      return;
    }
    setSaving(true);
    try {
      await api.setChannelBranchSurfing(channel.releaseChannelName, {
        enabled,
        pattern: trimmed,
      });
      await onUpdated();
      toast({
        title: enabled ? 'Branch surfing enabled' : 'Branch surfing disabled',
        description: enabled
          ? `Devices on "${channel.releaseChannelName}" can pick branches matching ${trimmed}.`
          : `Devices on "${channel.releaseChannelName}" are served the mapped branch only.`,
      });
    } catch (error) {
      const message = describeApiError(error, 'Could not update branch surfing');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
      setPattern(surfing.pattern);
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="rounded-lg border p-5">
      <div className="flex flex-col justify-between gap-4 sm:flex-row sm:items-start">
        <div className="min-w-0">
          <h2 className="flex items-center gap-2 text-sm font-semibold">
            <Compass className="h-4 w-4 text-muted-foreground" />
            Branch surfing
          </h2>
          <p className="mt-1 text-sm text-muted-foreground">
            {surfing.enabled
              ? 'Devices on this channel can ask to be served another branch.'
              : 'Devices on this channel are always served the branch it maps to.'}
          </p>
        </div>
        {canManage && (
          <Switch
            checked={surfing.enabled}
            disabled={saving || (!surfing.enabled && !canEnable)}
            onCheckedChange={next => void save(next, pattern)}
            aria-label={surfing.enabled ? 'Disable branch surfing' : 'Enable branch surfing'}
          />
        )}
      </div>

      <div className="mt-5 space-y-1.5 border-t pt-5">
        <Label htmlFor="branch-surfing-pattern">Branches exposed</Label>
        <div className="flex flex-col gap-2 sm:flex-row">
          <Input
            id="branch-surfing-pattern"
            className="font-mono sm:max-w-xs"
            value={pattern}
            disabled={!canManage || saving}
            onChange={event => setPattern(event.target.value)}
            placeholder="pr-*"
          />
          {canManage && (
            <Button
              variant="outline"
              disabled={saving || !patternChanged}
              onClick={() => void save(surfing.enabled, pattern)}>
              {saving ? 'Saving...' : 'Save'}
            </Button>
          )}
        </div>
        <p className="text-xs text-muted-foreground">
          <code className="font-mono">*</code> stands for any run of characters:{' '}
          <code className="font-mono">pr-*</code> exposes every branch starting with{' '}
          <code className="font-mono">pr-</code>.{' '}
          {!canEnable && !surfing.enabled && (
            <span className="text-foreground">Set it to turn branch surfing on.</span>
          )}
          {pattern.trim() === ALL_BRANCHES && (
            <span className="text-amber-600 dark:text-amber-500">
              Every branch of this app is exposed, including any created later. Devices
              read these names from an unauthenticated endpoint.
            </span>
          )}
        </p>
      </div>
    </section>
  );
};
