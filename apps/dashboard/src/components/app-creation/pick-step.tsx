import { Button } from '@/components/ui/button';
import { DialogFooter } from '@/components/ui/dialog';
import { SelectableCard } from './selectable-card';
import { AppCreation } from './use-app-creation';

export const PickStep = ({ creation }: { creation: AppCreation }) => {
  const accountsWithApps = (creation.accounts ?? []).filter(
    account => (account.apps?.length ?? 0) > 0
  );

  return (
    <div className="space-y-5 py-2">
      {accountsWithApps.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border bg-muted/20 p-4 text-sm text-muted-foreground">
          No apps were found on the accounts this token can access.
        </p>
      ) : (
        <div className="max-h-72 space-y-4 overflow-y-auto pr-1">
          {accountsWithApps.map(account => (
            <div key={account.accountId} className="space-y-2">
              <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                {account.accountName}
              </p>
              <div className="grid grid-cols-1 gap-2">
                {account.apps.map(app => (
                  <SelectableCard
                    key={app.id}
                    control="radio"
                    name="expoApp"
                    value={app.id}
                    selected={creation.selectedExpoApp?.id === app.id}
                    onSelect={() => creation.setSelectedExpoApp(app)}>
                    <span className="truncate text-sm font-medium text-foreground">
                      {app.name || app.fullName}
                    </span>
                    <span className="truncate font-mono text-xs text-muted-foreground">
                      {app.id}
                    </span>
                  </SelectableCard>
                ))}
              </div>
            </div>
          ))}
        </div>
      )}
      <DialogFooter className="pt-2 border-t border-border gap-2 sm:gap-0">
        <Button
          type="button"
          variant="outline"
          onClick={() => creation.setImportStep('token')}
          className="h-9 text-xs font-medium">
          Back
        </Button>
        <Button
          type="button"
          disabled={!creation.selectedExpoApp}
          onClick={() => creation.setImportStep('keys')}
          className="h-9">
          Continue
        </Button>
      </DialogFooter>
    </div>
  );
};
