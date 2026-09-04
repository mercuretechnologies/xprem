import { Button } from '@/components/ui/button';
import { DialogFooter } from '@/components/ui/dialog';
import { KeysModeSelector } from './keys-mode-selector';
import { SelectableCard } from './selectable-card';
import { AppCreation, HISTORY_LIMIT_CHOICES } from './use-app-creation';

export const KeysStep = ({ creation }: { creation: AppCreation }) => (
  <form onSubmit={creation.loadPlan} className="space-y-5 py-2">
    <div className="rounded-lg border border-border bg-muted/20 p-3">
      <p className="truncate text-sm font-medium text-foreground">
        {creation.selectedExpoApp?.name || creation.selectedExpoApp?.fullName}
      </p>
      <p className="truncate font-mono text-xs text-muted-foreground">
        {creation.selectedExpoApp?.id}
      </p>
    </div>
    <KeysModeSelector
      keysMode={creation.keysMode}
      setKeysMode={creation.setKeysMode}
      publicSecretId={creation.publicSecretId}
      setPublicSecretId={creation.setPublicSecretId}
      privateSecretId={creation.privateSecretId}
      setPrivateSecretId={creation.setPrivateSecretId}
      disabled={creation.isLoadingPlan}
    />
    <div className="space-y-2">
      <SelectableCard
        control="checkbox"
        selected={creation.includeHistory}
        onSelect={() => creation.setIncludeHistory(!creation.includeHistory)}
        disabled={creation.isLoadingPlan}>
        <span className="text-sm font-medium text-foreground">Also copy the update history</span>
        <span className="text-xs text-muted-foreground">
          Downloads the newest EAS updates into your storage, in the background. Code-signed
          updates are skipped.
        </span>
      </SelectableCard>
      {creation.includeHistory && (
        <div className="flex items-center gap-2 animate-in fade-in-50 duration-200">
          <span className="text-xs text-muted-foreground">Latest publishes to copy:</span>
          <div className="grid grid-cols-3 gap-1 rounded-lg border border-border bg-muted/30 p-1">
            {HISTORY_LIMIT_CHOICES.map(choice => (
              <button
                key={choice}
                type="button"
                disabled={creation.isLoadingPlan}
                onClick={() => creation.setHistoryLimit(choice)}
                className={`h-7 rounded-md px-3 text-xs font-medium transition-colors ${
                  creation.historyLimit === choice
                    ? 'bg-background text-foreground shadow-sm border border-border'
                    : 'text-muted-foreground hover:text-foreground'
                }`}>
                {choice}
              </button>
            ))}
          </div>
        </div>
      )}
    </div>
    <DialogFooter className="pt-2 border-t border-border gap-2 sm:gap-0">
      <Button
        type="button"
        variant="outline"
        onClick={() => creation.setImportStep('pick')}
        disabled={creation.isLoadingPlan}
        className="h-9 text-xs font-medium">
        Back
      </Button>
      <Button type="submit" disabled={creation.isLoadingPlan} className="h-9">
        {creation.isLoadingPlan ? 'Preparing preview…' : 'Continue'}
      </Button>
    </DialogFooter>
  </form>
);
