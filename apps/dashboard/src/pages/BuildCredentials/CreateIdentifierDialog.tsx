import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from 'react-router';
import { api, ApiProblemError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Badge } from '@/components/ui/badge';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { PlatformLogo } from './PlatformLogo';
import { PLATFORMS, Platform } from './platforms';

export const CreateIdentifierDialog = ({
  isOpen,
  onClose,
}: {
  isOpen: boolean;
  onClose: () => void;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const navigate = useNavigate();

  const [platform, setPlatform] = useState<Platform>('android');
  const [identifier, setIdentifier] = useState('');
  const [isCreating, setIsCreating] = useState(false);

  const handleClose = () => {
    if (isCreating) return;
    setIdentifier('');
    setPlatform('android');
    onClose();
  };

  const handleCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!identifier.trim()) return;
    setIsCreating(true);
    try {
      const { identifierId } = await api.createAppIdentifier({
        platform,
        identifier: identifier.trim(),
      });
      queryClient.invalidateQueries({ queryKey: ['identifiers', selectedAppId] });
      onClose();
      setIdentifier('');
      navigate(`/build-credentials/${identifierId}`);
    } catch (error) {
      let errorTitle = 'Error creating identifier';
      let errorMessage = 'An unexpected error occurred.';
      if (error instanceof ApiProblemError) {
        errorTitle = error.title;
        errorMessage = error.detail;
      } else if (error instanceof Error) {
        errorMessage = error.message;
      }
      toast({ title: errorTitle, description: errorMessage, variant: 'destructive' });
    } finally {
      setIsCreating(false);
    }
  };

  return (
    <Dialog open={isOpen} onOpenChange={open => !open && handleClose()}>
      <DialogContent className="sm:max-w-[480px]">
        <form onSubmit={handleCreate}>
          <DialogHeader>
            <DialogTitle>New application identifier</DialogTitle>
            <DialogDescription>
              An application identifier ties build credentials to one app on one platform. You
              will set up its credentials right after.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label>Platform</Label>
              <div className="grid grid-cols-2 gap-2">
                {PLATFORMS.map(section => {
                  const active = platform === section.platform;
                  return (
                    <button
                      key={section.platform}
                      type="button"
                      disabled={!section.enabled}
                      onClick={() => setPlatform(section.platform)}
                      aria-pressed={active}
                      className={`flex items-center justify-between rounded-lg border p-3 text-sm font-medium transition-colors ${
                        active
                          ? 'border-primary bg-primary/5 text-foreground'
                          : 'text-muted-foreground hover:bg-accent'
                      } disabled:cursor-not-allowed disabled:opacity-60 disabled:hover:bg-transparent`}>
                      <span className="flex items-center gap-2">
                        <PlatformLogo platform={section.platform} className="h-4 w-4" />
                        {section.label}
                      </span>
                      {!section.enabled && <Badge variant="secondary">Coming soon</Badge>}
                    </button>
                  );
                })}
              </div>
            </div>

            <div className="space-y-2">
              <Label htmlFor="new-identifier">Application identifier</Label>
              <Input
                id="new-identifier"
                placeholder="com.example.myapp"
                value={identifier}
                onChange={e => setIdentifier(e.target.value)}
                disabled={isCreating}
                autoFocus
              />
              <p className="text-xs text-muted-foreground">
                The Android application ID, as declared in your app manifest.
              </p>
            </div>
          </div>

          <DialogFooter className="gap-2 border-t pt-3 sm:gap-0">
            <Button type="button" variant="outline" onClick={handleClose} disabled={isCreating}>
              Cancel
            </Button>
            <Button type="submit" disabled={isCreating || !identifier.trim()}>
              {isCreating ? 'Creating…' : 'Create identifier'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
};
