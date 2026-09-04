import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { DialogFooter } from '@/components/ui/dialog';
import { KeysModeSelector } from './keys-mode-selector';
import { AppCreation } from './use-app-creation';

export const CreateStep = ({ creation }: { creation: AppCreation }) => (
  <form onSubmit={creation.submitCreate} className="space-y-5 py-2">
    <div className="space-y-1.5">
      <Label htmlFor="app-name">Name</Label>
      <Input
        id="app-name"
        placeholder="e.g. my-mobile-app"
        value={creation.name}
        onChange={e => creation.setName(e.target.value)}
        disabled={creation.isSubmitting}
        className="h-9"
        required
      />
    </div>
    <KeysModeSelector
      keysMode={creation.keysMode}
      setKeysMode={creation.setKeysMode}
      publicSecretId={creation.publicSecretId}
      setPublicSecretId={creation.setPublicSecretId}
      privateSecretId={creation.privateSecretId}
      setPrivateSecretId={creation.setPrivateSecretId}
      disabled={creation.isSubmitting}
    />
    <DialogFooter className="pt-2 border-t border-border gap-2 sm:gap-0">
      <Button
        type="button"
        variant="outline"
        onClick={creation.handleClose}
        disabled={creation.isSubmitting}
        className="h-9 text-xs font-medium">
        Cancel
      </Button>
      <Button type="submit" disabled={creation.isSubmitting} className="h-9">
        {creation.isSubmitting ? 'Creating…' : 'Create application'}
      </Button>
    </DialogFooter>
  </form>
);
