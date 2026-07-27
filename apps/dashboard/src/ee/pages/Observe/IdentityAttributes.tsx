import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Braces, KeyRound, Pencil, Plus, ShieldCheck, Trash2 } from 'lucide-react';
import { api, describeApiError, IdentitySchemaKey, IdentityValueType } from '@/lib/api';
import { useAppPermission } from '@/ee/lib/PermissionsContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { Skeleton } from '@/components/ui/skeleton';
import { EnterpriseFeatureGate } from '@/ee/components/EnterpriseFeatureGate';

type Draft = {
  originalKey?: string;
  key: string;
  type: IdentityValueType;
  maxLength: number;
};

const emptyDraft: Draft = { key: '', type: 'string', maxLength: 256 };

const typeStyle: Record<IdentityValueType, string> = {
  string: 'border-sky-400/20 bg-sky-400/10 text-sky-600 dark:text-sky-300',
  number: 'border-violet-400/20 bg-violet-400/10 text-violet-600 dark:text-violet-300',
  boolean: 'border-amber-400/20 bg-amber-400/10 text-amber-600 dark:text-amber-300',
};

export const IdentityAttributes = () => {
  const canManage = useAppPermission('identity:manage');
  const queryClient = useQueryClient();
  const { toast } = useToast();
  const [draft, setDraft] = useState<Draft | null>(null);
  const [deleting, setDeleting] = useState<IdentitySchemaKey | null>(null);

  const schemaQuery = useQuery({
    queryKey: ['identity', 'schema', api.getAppId()],
    queryFn: () => api.getIdentitySchema(),
  });

  const saveMutation = useMutation({
    mutationFn: (value: Draft) =>
      api.saveIdentitySchemaKey(value.key, {
        type: value.type,
        maxLength: value.type === 'string' ? value.maxLength : 256,
      }),
    onSuccess: saved => {
      queryClient.invalidateQueries({ queryKey: ['identity', 'schema'] });
      setDraft(null);
      toast({
        title: 'Identity attribute saved',
        description: `"${saved.key}" is now accepted by $set and available as an Observe filter.`,
      });
    },
    onError: error =>
      toast({ ...describeApiError(error, 'Could not save attribute'), variant: 'destructive' }),
  });

  const deleteMutation = useMutation({
    mutationFn: (key: string) => api.deleteIdentitySchemaKey(key),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['identity', 'schema'] });
      setDeleting(null);
      toast({ title: 'Identity attribute deleted' });
    },
    onError: error =>
      toast({ ...describeApiError(error, 'Could not delete attribute'), variant: 'destructive' }),
  });

  const keys = schemaQuery.data?.keys ?? [];

  return (
    <div className="space-y-5">
      <EnterpriseFeatureGate>
        <section className="overflow-hidden rounded-xl border bg-card shadow-card">
          <div className="flex flex-col gap-4 border-b px-5 py-5 sm:flex-row sm:items-start sm:justify-between">
            <div className="max-w-2xl">
              <div className="flex items-center gap-2">
                <Braces className="h-4 w-4 text-primary" />
                <h2 className="font-display text-base font-semibold">Identity attributes</h2>
              </div>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                Declare the metadata accepted from <code>$set</code>, <code>$set_once</code> and{' '}
                <code>$unset</code>. Undeclared keys and mismatched values are discarded at
                ingestion.
              </p>
            </div>
            {canManage && (
              <Button onClick={() => setDraft(emptyDraft)}>
                <Plus />
                Add attribute
              </Button>
            )}
          </div>

          <div className="grid grid-cols-[minmax(0,1fr)_110px_110px_80px] border-b bg-muted/30 px-5 py-2.5 text-[11px] font-medium text-muted-foreground">
            <span>Key</span>
            <span>Type</span>
            <span>Constraint</span>
            <span className="text-right">Actions</span>
          </div>

          {schemaQuery.isLoading &&
            [0, 1, 2].map(row => (
              <div
                key={row}
                className="grid grid-cols-[minmax(0,1fr)_110px_110px_80px] items-center border-b px-5 py-4 last:border-0">
                <Skeleton className="h-4 w-40" />
                <Skeleton className="h-6 w-16" />
                <Skeleton className="h-4 w-16" />
                <Skeleton className="ml-auto h-8 w-16" />
              </div>
            ))}

          {/* A failed read has no keys either, so it would otherwise fall into
              the empty state and claim the allowlist is empty. Someone acting
              on that would start declaring keys that already exist. */}
          {schemaQuery.isError && (
            <div className="flex flex-col items-center px-6 py-16 text-center">
              <div className="flex h-11 w-11 items-center justify-center rounded-xl border bg-secondary text-muted-foreground">
                <KeyRound className="h-5 w-5" />
              </div>
              <h3 className="mt-4 text-sm font-medium">Could not load Identity attributes</h3>
              <p className="mt-1 max-w-md text-sm text-muted-foreground">
                The server did not answer, so the declared keys are unknown. This is not the same
                as an empty allowlist.
              </p>
            </div>
          )}

          {!schemaQuery.isLoading && !schemaQuery.isError && keys.length === 0 && (
            <div className="flex flex-col items-center px-6 py-16 text-center">
              <div className="flex h-11 w-11 items-center justify-center rounded-xl border bg-secondary text-muted-foreground">
                <KeyRound className="h-5 w-5" />
              </div>
              <h3 className="mt-4 text-sm font-medium">No Identity attributes yet</h3>
              <p className="mt-1 max-w-md text-sm text-muted-foreground">
                Add keys such as <code>userId</code>, <code>tenant</code> or <code>plan</code>{' '}
                before sending them from the app.
              </p>
            </div>
          )}

          {keys.map(spec => (
            <div
              key={spec.key}
              className="grid grid-cols-[minmax(0,1fr)_110px_110px_80px] items-center border-b px-5 py-3.5 last:border-0">
              <div className="min-w-0">
                <code className="truncate font-mono text-[13px] font-medium text-foreground">
                  {spec.key}
                </code>
              </div>
              <span
                className={`w-fit rounded-full border px-2 py-0.5 font-mono text-[11px] ${typeStyle[spec.type]}`}>
                {spec.type}
              </span>
              <span className="font-mono text-xs text-muted-foreground">
                {spec.type === 'string' ? `${spec.maxLength} chars` : 'typed'}
              </span>
              <div className="flex justify-end gap-1">
                {canManage && (
                  <>
                    <Button
                      variant="ghost"
                      size="icon"
                      aria-label={`Edit ${spec.key}`}
                      onClick={() =>
                        setDraft({
                          originalKey: spec.key,
                          key: spec.key,
                          type: spec.type,
                          maxLength: spec.maxLength,
                        })
                      }>
                      <Pencil />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="text-muted-foreground hover:text-destructive"
                      aria-label={`Delete ${spec.key}`}
                      onClick={() => setDeleting(spec)}>
                      <Trash2 />
                    </Button>
                  </>
                )}
              </div>
            </div>
          ))}
        </section>
      </EnterpriseFeatureGate>

      <div className="flex gap-3 rounded-xl border border-emerald-400/20 bg-emerald-400/[0.06] px-4 py-3 text-sm text-emerald-800 dark:text-emerald-200">
        <ShieldCheck className="mt-0.5 h-4 w-4 shrink-0" />
        <p>
          Identity values stay in PostgreSQL. Observe only sends the matching active cohort to
          ClickHouse while executing a filtered query.
        </p>
      </div>

      <Dialog open={draft != null} onOpenChange={open => !open && setDraft(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{draft?.originalKey ? 'Edit attribute' : 'Add attribute'}</DialogTitle>
            <DialogDescription>
              The key and type become the ingestion allowlist and the filter definition.
            </DialogDescription>
          </DialogHeader>
          {draft && (
            <form
              className="space-y-4"
              onSubmit={event => {
                event.preventDefault();
                saveMutation.mutate(draft);
              }}>
              <label className="block space-y-1.5">
                <span className="text-xs font-medium text-muted-foreground">Key</span>
                <Input
                  autoFocus
                  disabled={Boolean(draft.originalKey)}
                  value={draft.key}
                  pattern="[A-Za-z0-9][A-Za-z0-9_.-]{0,63}"
                  placeholder="tenant.plan"
                  onChange={event => setDraft({ ...draft, key: event.target.value })}
                />
              </label>
              <label className="block space-y-1.5">
                <span className="text-xs font-medium text-muted-foreground">Type</span>
                <select
                  value={draft.type}
                  onChange={event =>
                    setDraft({ ...draft, type: event.target.value as IdentityValueType })
                  }
                  className="h-9 w-full rounded-md border border-input bg-card px-3 text-sm text-foreground outline-none focus:border-ring focus:ring-2 focus:ring-ring/20">
                  <option value="string">String</option>
                  <option value="number">Number</option>
                  <option value="boolean">Boolean</option>
                </select>
              </label>
              {draft.type === 'string' && (
                <label className="block space-y-1.5">
                  <span className="text-xs font-medium text-muted-foreground">Maximum length</span>
                  <Input
                    type="number"
                    min={1}
                    max={1024}
                    value={draft.maxLength}
                    onChange={event =>
                      setDraft({ ...draft, maxLength: Number(event.target.value) })
                    }
                  />
                </label>
              )}
              <DialogFooter>
                <Button type="button" variant="outline" onClick={() => setDraft(null)}>
                  Cancel
                </Button>
                <Button type="submit" disabled={!draft.key || saveMutation.isPending || !canManage}>
                  {saveMutation.isPending ? 'Saving…' : 'Save attribute'}
                </Button>
              </DialogFooter>
            </form>
          )}
        </DialogContent>
      </Dialog>

      <DeleteDialog
        isOpen={deleting != null}
        onClose={() => setDeleting(null)}
        onConfirm={() => {
          if (deleting) deleteMutation.mutate(deleting.key);
        }}
        isDeleting={deleteMutation.isPending}
        title="Delete Identity attribute"
        resourceName={deleting?.key}
        descriptionText="New values for this key will be discarded. Existing device metadata is preserved."
      />
    </div>
  );
};
