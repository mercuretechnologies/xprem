import { useState } from 'react';
import { ChevronDown } from 'lucide-react';
import { QRCodeSVG } from 'qrcode.react';
import { api, CreateIosDeviceInvitationResponse, describeApiError } from '@/lib/api';
import { formatTimestamp } from '@/lib/utils';
import { useToast } from '@/hooks/use-toast';
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
import { CopyLinkButton } from '@/pages/Builds/components/ShareBuildDialog';

const EXPIRY_OPTIONS = [
  { hours: 24, label: '1 day' },
  { hours: 168, label: '7 days' },
  { hours: 720, label: '30 days' },
];

/** Creates an expiring single-use invitation and shows its link and QR code once. */
export const InviteIphonesDialog = ({
  isOpen,
  onClose,
  onCreated,
}: {
  isOpen: boolean;
  onClose: () => void;
  onCreated: () => void;
}) => {
  const { toast } = useToast();
  const [label, setLabel] = useState('');
  const [expiresInHours, setExpiresInHours] = useState(168);
  const [isCreating, setIsCreating] = useState(false);
  // Held for the life of the dialog only: the server never hands the URL back a second time.
  const [created, setCreated] = useState<CreateIosDeviceInvitationResponse | null>(null);

  /** Clears the generated registration link when the invitation dialog closes. */
  const handleClose = () => {
    if (isCreating) return;
    setLabel('');
    setExpiresInHours(168);
    setCreated(null);
    onClose();
  };

  /** Creates an invitation using the selected label and expiration period. */
  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    setIsCreating(true);
    try {
      setCreated(await api.createIosDeviceInvitation({ label: label.trim(), expiresInHours }));
      onCreated();
    } catch (error) {
      const message = describeApiError(error, 'Could not create registration link');
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
              <DialogTitle>Registration link ready</DialogTitle>
              <DialogDescription>
                Open this link on the iPhone, in Safari. It is shown once: copy it now.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4 py-2">
              <div className="flex justify-center rounded-lg border bg-white p-4">
                <QRCodeSVG value={created.url} size={192} level="M" marginSize={0} />
              </div>

              <div className="space-y-2">
                <Label htmlFor="registration-link">Link</Label>
                <div className="flex gap-2">
                  <Input
                    id="registration-link"
                    readOnly
                    value={created.url}
                    onFocus={e => e.currentTarget.select()}
                    className="font-mono text-xs"
                  />
                  <CopyLinkButton url={created.url} />
                </div>
              </div>

              <p className="text-sm text-muted-foreground">
                Expires {formatTimestamp(created.invitation.expiresAt)}. You can revoke it earlier
                from the pending invitations.
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
              <DialogTitle>Invite an iPhone</DialogTitle>
              <DialogDescription>
                The link registers one iPhone. Create one link per iPhone.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-4 py-4">
              <div className="space-y-2">
                <Label htmlFor="invitation-label">Name (optional)</Label>
                <Input
                  id="invitation-label"
                  placeholder="Marie's iPhone"
                  value={label}
                  onChange={e => setLabel(e.target.value)}
                  disabled={isCreating}
                  autoFocus
                />
              </div>
              <div className="space-y-2">
                <Label htmlFor="invitation-expiry">Valid for</Label>
                <div className="relative w-40">
                  <select
                    id="invitation-expiry"
                    value={expiresInHours}
                    onChange={e => setExpiresInHours(Number(e.target.value))}
                    disabled={isCreating}
                    className="block h-9 w-full appearance-none rounded-md border border-input bg-card pl-3 pr-9 text-sm text-foreground outline-none focus:border-ring focus:ring-2 focus:ring-ring/20 disabled:opacity-50">
                    {EXPIRY_OPTIONS.map(option => (
                      <option key={option.hours} value={option.hours}>
                        {option.label}
                      </option>
                    ))}
                  </select>
                  <ChevronDown className="pointer-events-none absolute right-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
                </div>
              </div>
            </div>

            <DialogFooter className="gap-2 border-t pt-3 sm:gap-0">
              <Button type="button" variant="outline" onClick={handleClose} disabled={isCreating}>
                Cancel
              </Button>
              <Button type="submit" disabled={isCreating}>
                {isCreating ? 'Creating…' : 'Create link'}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
};
