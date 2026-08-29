import { Button } from '@/components/ui/button';
import { DialogFooter } from '@/components/ui/dialog';
import { AppCreation } from './use-app-creation';

export const HistoryStep = ({ creation }: { creation: AppCreation }) => {
  const status = creation.historyStatus;
  const isRunning = !status || status.state === 'running';
  const cancelRequested = creation.historyCancelRequested || status?.cancelRequested;

  return (
    <div className="space-y-5 py-2">
      <div className="rounded-lg border border-border bg-muted/20 p-4 space-y-3">
        {isRunning && (
          <>
            <p className="text-sm font-medium text-foreground">Copying update history…</p>
            <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-primary transition-all duration-500"
                style={{
                  width: status?.total
                    ? `${Math.round((status.processed / status.total) * 100)}%`
                    : '5%',
                }}
              />
            </div>
            <p className="text-xs text-muted-foreground">
              {status
                ? `${status.processed} of ${status.total} updates processed, ${status.imported} copied.`
                : 'Starting…'}
            </p>
          </>
        )}
        {status?.state === 'done' && (
          <>
            <p className="text-sm font-medium text-foreground">Update history copied</p>
            <p className="text-xs text-muted-foreground">
              {status.imported} update{status.imported === 1 ? '' : 's'} imported
              {status.skipped?.length ? `, ${status.skipped.length} skipped.` : '.'}
            </p>
          </>
        )}
        {status?.state === 'failed' && (
          <>
            <p className="text-sm font-medium text-destructive">History import failed</p>
            <p className="text-xs text-muted-foreground break-all">
              {status.error || 'The import stopped on an internal error.'}
              {status.imported > 0 && ` ${status.imported} updates were copied before the failure.`}
            </p>
          </>
        )}
        {status?.state === 'canceled' && (
          <>
            <p className="text-sm font-medium text-foreground">History import canceled</p>
            <p className="text-xs text-muted-foreground">
              {status.imported} update{status.imported === 1 ? '' : 's'} had already been copied
              and stay available.
            </p>
          </>
        )}
        {status?.skipped && status.skipped.length > 0 && (
          <div className="max-h-32 space-y-1 overflow-y-auto rounded-md border border-dashed border-border bg-background/50 p-2">
            {status.skipped.map(entry => (
              <p key={entry} className="text-xs text-muted-foreground break-all">
                {entry}
              </p>
            ))}
          </div>
        )}
      </div>
      {isRunning && (
        <p className="text-xs text-muted-foreground">
          You can close this window, the import keeps running in the background.
        </p>
      )}
      <DialogFooter className="pt-2 border-t border-border gap-2">
        {isRunning && (
          <Button
            type="button"
            variant="ghost"
            onClick={creation.cancelHistoryImport}
            disabled={cancelRequested}
            className="h-9 text-xs">
            {cancelRequested ? 'Stopping…' : 'Cancel import'}
          </Button>
        )}
        <Button
          type="button"
          onClick={creation.handleClose}
          variant={!isRunning ? 'default' : 'outline'}
          className="h-9">
          {!isRunning ? 'Done' : 'Close'}
        </Button>
      </DialogFooter>
    </div>
  );
};
