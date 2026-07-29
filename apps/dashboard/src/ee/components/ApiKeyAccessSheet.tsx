// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { useState } from 'react';
import { Plus, Trash2 } from 'lucide-react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  api,
  ApiKeyRecord,
  ApiKeyAccessRecord,
  BranchRuleAction,
  BranchRuleRecord,
  describeApiError,
} from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet';
import { EnterpriseFeatureGate } from '@/ee/components/EnterpriseFeatureGate';
import { BranchPatternInput } from '@/ee/components/BranchPatternInput';
import { cn } from '@/lib/utils';

// Side panel to edit what one API token is allowed to do: which branches it
// reaches and with which actions, whether it may open a branch that does not
// exist yet, and the source addresses it may be used from. Without a valid
// license the form is masked by EnterpriseFeatureGate.
export const ApiKeyAccessSheet = ({
  apiKey,
  onClose,
}: {
  apiKey: ApiKeyRecord | null;
  onClose: () => void;
}) => {
  const { selectedAppId } = useSelectedApp();

  const accessQuery = useQuery({
    queryKey: ['apiKeyAccess', selectedAppId],
    queryFn: () => api.getApiKeyAccess(),
    enabled: !!selectedAppId,
  });

  return (
    <Sheet open={!!apiKey} onOpenChange={open => !open && onClose()}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>Token access</SheetTitle>
          <SheetDescription>
            Choose which branches “{apiKey?.name}” reaches and what it may do on each of them.
          </SheetDescription>
        </SheetHeader>
        <div className="mt-6">
          <EnterpriseFeatureGate>
            {accessQuery.isLoading ? (
              <div className="space-y-3">
                <Skeleton className="h-12 w-full" />
                <Skeleton className="h-4 w-1/2" />
                <Skeleton className="h-24 w-full" />
              </div>
            ) : accessQuery.isError ? (
              <div className="space-y-3 text-sm text-muted-foreground">
                <p>Could not load what this token is allowed to do.</p>
                <Button variant="outline" onClick={() => accessQuery.refetch()}>
                  Try again
                </Button>
              </div>
            ) : (
              apiKey && (
                <AccessForm
                  // Remount when switching tokens so the form state resets to
                  // the stored access of the newly opened token.
                  key={apiKey.id}
                  apiKey={apiKey}
                  initialAccess={accessQuery.data?.find(access => access.apiKeyId === apiKey.id)}
                  onSaved={onClose}
                />
              )
            )}
          </EnterpriseFeatureGate>
        </div>
      </SheetContent>
    </Sheet>
  );
};

const ACTION_LABELS: { value: BranchRuleAction; label: string; hint: string }[] = [
  { value: 'read', label: 'Read', hint: 'List runtime versions and shipped updates' },
  { value: 'publish', label: 'Publish', hint: 'Ship a new update' },
  { value: 'rollback', label: 'Rollback', hint: 'Roll back, and republish a past update' },
];

const AccessForm = ({
  apiKey,
  initialAccess,
  onSaved,
}: {
  apiKey: ApiKeyRecord;
  initialAccess?: ApiKeyAccessRecord;
  onSaved: () => void;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const branchesQuery = useQuery({
    queryKey: ['branches', selectedAppId],
    queryFn: () => api.getBranches(),
    enabled: !!selectedAppId,
  });
  const branches = (branchesQuery.data ?? []).map(branch => branch.branchName);

  const initialRules = initialAccess?.branchRules ?? [];
  // No rule stored means every branch, which is what a fresh token holds.
  const [isScoped, setIsScoped] = useState(initialRules.length > 0);
  const [rules, setRules] = useState<BranchRuleRecord[]>(initialRules);
  const [allowedIpsText, setAllowedIpsText] = useState(
    (initialAccess?.allowedIps ?? []).join('\n')
  );
  const [error, setError] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);

  const updateRule = (index: number, patch: Partial<BranchRuleRecord>) => {
    setRules(current => current.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));
  };

  const toggleAction = (index: number, action: BranchRuleAction) => {
    const rule = rules[index];
    const next = rule.actions.includes(action)
      ? rule.actions.filter(granted => granted !== action)
      : [...rule.actions, action];
    updateRule(index, { actions: next });
  };

  // Checked client-side so the operator sees the problem next to the field
  // rather than as a toast carrying the server's version of it.
  const validate = (): string | null => {
    if (!isScoped) return null;
    if (rules.length === 0) return 'Add at least one rule, or let the token reach every branch.';
    const seen = new Set<string>();
    for (const rule of rules) {
      const pattern = rule.pattern.trim();
      if (!pattern) return 'Every rule needs a branch name or a pattern.';
      if (pattern.includes('/') || pattern.includes('\\')) {
        return `“${pattern}” cannot contain a slash: a branch name is a single segment.`;
      }
      if (seen.has(pattern)) return `“${pattern}” appears twice. Merge the two rules into one.`;
      seen.add(pattern);
      if (rule.actions.length === 0) {
        return `“${pattern}” grants nothing. Pick an action, or remove the rule.`;
      }
    }
    return null;
  };

  const handleSave = async () => {
    const validationError = validate();
    setError(validationError);
    if (validationError) return;

    setIsSaving(true);
    try {
      const allowedIps = allowedIpsText
        .split('\n')
        .map(line => line.trim())
        .filter(Boolean);
      await api.setApiKeyAccess(apiKey.id, {
        // Unscoped is stored as an empty list, which the server reads as every
        // branch. The rules kept in local state are not sent in that case.
        branchRules: isScoped ? rules.map(rule => ({ ...rule, pattern: rule.pattern.trim() })) : [],
        allowedIps,
      });
      queryClient.invalidateQueries({ queryKey: ['apiKeyAccess', selectedAppId] });
      toast({
        title: 'Access saved',
        description: `“${apiKey.name}” now uses the updated access.`,
      });
      onSaved();
    } catch (caught) {
      const { title, description } = describeApiError(caught, 'Could not save this token’s access');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <p className="text-sm font-medium">Branches</p>
        <div className="grid gap-2">
          <ScopeChoice
            selected={!isScoped}
            onSelect={() => setIsScoped(false)}
            title="Every branch"
            description="The token can read, publish and roll back anywhere in this app."
          />
          <ScopeChoice
            selected={isScoped}
            onSelect={() => {
              setIsScoped(true);
              if (rules.length === 0) setRules([{ pattern: '', actions: ['read', 'publish'] }]);
            }}
            title="Only the branches I list"
            description="Anything not listed is refused, including branches created later."
          />
        </div>
      </div>

      {isScoped && (
        <div className="space-y-3">
          {rules.map((rule, index) => (
            <div key={index} className="space-y-2.5 rounded-lg border p-3">
              <div className="flex items-start gap-2">
                <div className="min-w-0 flex-1">
                  <BranchPatternInput
                    value={rule.pattern}
                    onChange={pattern => updateRule(index, { pattern })}
                    branches={branches}
                    disabled={isSaving}
                  />
                </div>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-9 w-9 shrink-0 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                  title="Remove this rule"
                  disabled={isSaving}
                  onClick={() => setRules(current => current.filter((_, i) => i !== index))}>
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {ACTION_LABELS.map(action => {
                  const isGranted = rule.actions.includes(action.value);
                  // Both writes imply read on the server, so showing read as
                  // granted here is the truth rather than a convenience.
                  const impliedByWrite =
                    action.value === 'read' &&
                    (rule.actions.includes('publish') || rule.actions.includes('rollback'));
                  return (
                    <button
                      key={action.value}
                      type="button"
                      title={action.hint}
                      disabled={isSaving || impliedByWrite}
                      onClick={() => toggleAction(index, action.value)}
                      className={cn(
                        'rounded-md border px-2.5 py-1 text-xs font-medium transition-colors',
                        isGranted || impliedByWrite
                          ? 'border-primary/40 bg-primary/10 text-primary'
                          : 'text-muted-foreground hover:bg-muted/50',
                        impliedByWrite && 'cursor-default opacity-80'
                      )}>
                      {action.label}
                      {impliedByWrite && ' (implied)'}
                    </button>
                  );
                })}
              </div>
            </div>
          ))}
          <Button
            variant="outline"
            size="sm"
            disabled={isSaving}
            onClick={() =>
              setRules(current => [...current, { pattern: '', actions: ['publish'] }])
            }>
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Add a rule
          </Button>
        </div>
      )}

      <div className="space-y-2">
        <p className="text-sm font-medium">IP allowlist</p>
        <p className="text-xs text-muted-foreground">
          One address or CIDR range per line, for example 203.0.113.7 or 203.0.113.0/24. Leave empty
          to allow any source address.
        </p>
        <textarea
          value={allowedIpsText}
          onChange={event => setAllowedIpsText(event.target.value)}
          placeholder={'203.0.113.0/24\n2001:db8::/32'}
          rows={4}
          spellCheck={false}
          disabled={isSaving}
          className="w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        />
      </div>

      {error && <p className="text-sm text-destructive">{error}</p>}

      <div className="flex justify-end">
        <Button onClick={handleSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save access'}
        </Button>
      </div>
    </div>
  );
};

const ScopeChoice = ({
  selected,
  onSelect,
  title,
  description,
}: {
  selected: boolean;
  onSelect: () => void;
  title: string;
  description: string;
}) => (
  <button
    type="button"
    onClick={onSelect}
    className={cn(
      'rounded-lg border px-3 py-2.5 text-left transition-colors',
      selected ? 'border-primary/50 bg-primary/5' : 'hover:bg-muted/40'
    )}>
    <span className="block text-sm font-medium">{title}</span>
    <span className="mt-0.5 block text-xs text-muted-foreground">{description}</span>
  </button>
);
