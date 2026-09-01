import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { DialogFooter } from '@/components/ui/dialog';
import { AppCreation } from './use-app-creation';

export const TokenStep = ({ creation }: { creation: AppCreation }) => (
  <form
    onSubmit={e => {
      e.preventDefault();
      creation.loadExpoApps();
    }}
    className="space-y-5 py-2">
    <div className="space-y-1.5">
      <Label htmlFor="expo-token">Expo access token</Label>
      <Input
        id="expo-token"
        type="password"
        placeholder="Paste your token"
        value={creation.accessToken}
        onChange={e => creation.setAccessToken(e.target.value)}
        disabled={creation.isLoadingApps}
        className="h-9"
        autoComplete="off"
        required
      />
      <p className="text-xs text-muted-foreground">
        Create one under{' '}
        <a
          href="https://expo.dev/settings/access-tokens"
          target="_blank"
          rel="noreferrer"
          className="underline underline-offset-2 hover:text-foreground">
          expo.dev → Access tokens
        </a>
        .
      </p>
    </div>
    <DialogFooter className="pt-2 border-t border-border gap-2 sm:gap-0">
      <Button
        type="button"
        variant="outline"
        onClick={creation.handleClose}
        disabled={creation.isLoadingApps}
        className="h-9 text-xs font-medium">
        Cancel
      </Button>
      <Button type="submit" disabled={creation.isLoadingApps} className="h-9">
        {creation.isLoadingApps ? 'Loading apps…' : 'Continue'}
      </Button>
    </DialogFooter>
  </form>
);
