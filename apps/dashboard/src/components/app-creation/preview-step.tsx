import { Check, CircleSlash, TriangleAlert } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { DialogFooter } from '@/components/ui/dialog';
import { ExpoImportPlanItem } from '@/lib/api';
import { AppCreation } from './use-app-creation';

const PlanList = ({ label, items }: { label: string; items: ExpoImportPlanItem[] }) => {
  const importedCount = items.filter(item => !item.skipReason).length;
  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label} · {importedCount}/{items.length} imported
      </p>
      {items.length === 0 ? (
        <p className="rounded-lg border border-border p-2 text-xs text-muted-foreground">
          None on this project.
        </p>
      ) : (
        <ul className="max-h-36 space-y-1 overflow-y-auto rounded-lg border border-border p-2">
          {items.map(item => (
            <li key={item.name} className="flex items-start gap-2 text-sm">
              {item.skipReason ? (
                <CircleSlash className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive" />
              ) : item.warning ? (
                <TriangleAlert className="mt-0.5 h-3.5 w-3.5 shrink-0 text-amber-500" />
              ) : (
                <Check className="mt-0.5 h-3.5 w-3.5 shrink-0 text-emerald-500" />
              )}
              <div className="min-w-0 flex-1">
                <p className="truncate font-medium text-foreground">
                  {item.name}
                  {item.mappedBranch && (
                    <span className="ml-1.5 font-normal text-muted-foreground">
                      → {item.mappedBranch}
                    </span>
                  )}
                </p>
                {(item.skipReason || item.warning) && (
                  <p className="text-xs text-muted-foreground">
                    {item.skipReason ? `Skipped: ${item.skipReason}` : item.warning}
                  </p>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
};

export const PreviewStep = ({ creation }: { creation: AppCreation }) => {
  const { plan } = creation;
  if (!plan) {
    return null;
  }
  return (
    <div className="space-y-4 py-2">
      {plan.conflict ? (
        <div className="rounded-lg border border-destructive/50 bg-destructive/10 p-3">
          <p className="text-sm font-medium text-destructive">This app cannot be imported</p>
          <p className="mt-1 text-xs text-destructive/90">{plan.conflict}</p>
        </div>
      ) : (
        <div className="space-y-1 rounded-lg border border-border bg-muted/20 p-3">
          <p className="truncate text-sm font-medium text-foreground">{plan.name}</p>
          {plan.name !== plan.expoName && (
            <p className="text-xs text-muted-foreground">
              Renamed: “{plan.expoName}” is not a valid app name here.
            </p>
          )}
          <p className="truncate font-mono text-xs text-muted-foreground">{plan.appId}</p>
          <p className="text-xs text-muted-foreground">
            Created with the same UUID as on Expo, so devices keep recognizing the updates they
            already run.
          </p>
        </div>
      )}

      <PlanList label="Branches" items={plan.branches} />
      <PlanList label="Channels" items={plan.channels} />

      {creation.includeHistory && !plan.conflict && (
        <p className="text-xs text-muted-foreground">
          The {creation.historyLimit} newest publishes will then be copied in the background.
        </p>
      )}

      <DialogFooter className="gap-2 border-t border-border pt-2 sm:gap-0">
        <Button
          type="button"
          variant="outline"
          onClick={() => creation.setImportStep('keys')}
          disabled={creation.isSubmitting}
          className="h-9 text-xs font-medium">
          Back
        </Button>
        <Button
          type="button"
          onClick={creation.submitImport}
          disabled={creation.isSubmitting || Boolean(plan.conflict)}
          className="h-9">
          {creation.isSubmitting ? 'Importing…' : 'Import application'}
        </Button>
      </DialogFooter>
    </div>
  );
};
