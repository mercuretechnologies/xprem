import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { QRCodeSVG } from 'qrcode.react';
import { Check, Copy } from 'lucide-react';
import { api, BuildRecord, CreateBuildShareResponse, describeApiError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { formatTimestamp } from '@/lib/utils';
import { Button } from '@/components/ui/button';
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
import { buildVersionLabel, DEFAULT_SHARE_HOURS, MAX_SHARE_HOURS } from '../format';

const parseHours = (value: string): number | null => {
  if (!/^\d+$/.test(value.trim())) return null;
  const hours = Number(value);
  if (hours < 1 || hours > MAX_SHARE_HOURS) return null;
  return hours;
};

export const CopyLinkButton = ({ url }: { url: string }) => {
  const { toast } = useToast();
  const [copied, setCopied] = useState(false);
  return (
    <Button
      type="button"
      variant="outline"
      className="shrink-0"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(url);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          toast({
            title: 'Could not copy',
            description: 'Select the link and copy it manually.',
            variant: 'destructive',
          });
        }
      }}>
      {copied ? (
        <Check className="h-4 w-4 text-emerald-700 dark:text-emerald-300" />
      ) : (
        <Copy className="h-4 w-4" />
      )}
      {copied ? 'Copied' : 'Copy link'}
    </Button>
  );
};

export const ShareBuildDialog = ({
  build,
  isOpen,
  onClose,
}: {
  build: BuildRecord;
  isOpen: boolean;
  onClose: () => void;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const [hoursInput, setHoursInput] = useState(String(DEFAULT_SHARE_HOURS));
  const [isCreating, setIsCreating] = useState(false);
  // Held for the life of the dialog only: the server never hands the URL
  // back a second time.
  const [created, setCreated] = useState<CreateBuildShareResponse | null>(null);

  const hours = parseHours(hoursInput);

  const handleClose = () => {
    if (isCreating) return;
    setHoursInput(String(DEFAULT_SHARE_HOURS));
    setCreated(null);
    onClose();
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (hours === null) return;
    setIsCreating(true);
    try {
      const response = await api.createBuildShare(build.id, hours);
      setCreated(response);
      await queryClient.invalidateQueries({ queryKey: ['build-shares', selectedAppId, build.id] });
    } catch (error) {
      const message = describeApiError(error, 'Could not create install link');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsCreating(false);
    }
  };

  return (
    <Dialog open={isOpen} onOpenChange={open => !open && handleClose()}>
      <DialogContent className="sm:max-w-[480px]">
        {created ? (
          <>
            <DialogHeader>
              <DialogTitle>Install link ready</DialogTitle>
              <DialogDescription>
                Scan the code or send the link. Anyone holding it can install{' '}
                {buildVersionLabel(build)} of {build.applicationId} until it expires. It is shown
                once: copy it now.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4 py-2">
              <div className="flex justify-center rounded-lg border bg-white p-4">
                <QRCodeSVG value={created.url} size={192} level="M" marginSize={0} />
              </div>

              <div className="space-y-2">
                <Label htmlFor="install-link">Link</Label>
                <div className="flex gap-2">
                  <Input
                    id="install-link"
                    readOnly
                    value={created.url}
                    onFocus={e => e.currentTarget.select()}
                    className="font-mono text-xs"
                  />
                  <CopyLinkButton url={created.url} />
                </div>
              </div>

              <p className="text-sm text-muted-foreground">
                Expires {formatTimestamp(created.share.expiresAt)}. You can revoke it earlier from
                the install links list.
              </p>
            </div>

            <DialogFooter className="gap-2 border-t pt-3 sm:gap-0">
              <Button type="button" onClick={handleClose}>
                Done
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form onSubmit={handleCreate}>
            <DialogHeader>
              <DialogTitle>New install link</DialogTitle>
              <DialogDescription>
                Creates a link and a QR code that install {buildVersionLabel(build)} of{' '}
                {build.applicationId} on any Android device, without signing in.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4 py-4">
              <div className="space-y-2">
                <Label htmlFor="share-hours">Valid for (hours)</Label>
                <Input
                  id="share-hours"
                  type="text"
                  inputMode="numeric"
                  value={hoursInput}
                  onChange={e => setHoursInput(e.target.value)}
                  disabled={isCreating}
                  className="w-40"
                  autoFocus
                />
                <p className="text-xs text-muted-foreground">
                  Between 1 and {MAX_SHARE_HOURS} hours (30 days). The link stops working when it
                  expires or when you revoke it.
                </p>
              </div>
            </div>

            <DialogFooter className="gap-2 border-t pt-3 sm:gap-0">
              <Button type="button" variant="outline" onClick={handleClose} disabled={isCreating}>
                Cancel
              </Button>
              <Button type="submit" disabled={isCreating || hours === null}>
                {isCreating ? 'Creating…' : 'Create link'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
};
