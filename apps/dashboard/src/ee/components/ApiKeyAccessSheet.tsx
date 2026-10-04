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
  UpdateAction,
  BuildAction,
  BuildRuleRecord,
  SubmitRuleRecord,
  SubmitDestination,
  AppIdentifier,
  EnvironmentRuleRecord,
  UpdateRuleRecord,
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
import { tokenAccessFeature } from '@/ee/lib/enterpriseFeatures';
import { BranchPatternInput, NamePatternInput } from '@/ee/components/BranchPatternInput';
import { cn } from '@/lib/utils';

// Edit Updates rules, Build, Submit and Environment permissions and source IPs independently.
// Without a valid license the form is masked by EnterpriseFeatureGate.
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
            Choose what “{apiKey?.name}” can do in Updates, Build and Submit, and which
            environments it can read.
          </SheetDescription>
        </SheetHeader>
        <div className="mt-6">
          <EnterpriseFeatureGate feature={tokenAccessFeature}>
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

const ACTION_LABELS: { value: UpdateAction; label: string; hint: string }[] = [
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

  const initialRules = initialAccess?.updates.rules ?? [];
  const [updatesMode, setUpdatesMode] = useState<'none' | 'all' | 'custom'>(
    initialRules.length === 0
      ? 'none'
      : initialRules.some(
            rule =>
              rule.pattern === '*' &&
              rule.actions.includes('publish') &&
              rule.actions.includes('rollback')
          )
        ? 'all'
        : 'custom'
  );
  const [rules, setRules] = useState<UpdateRuleRecord[]>(initialRules);
  const [buildRules, setBuildRules] = useState<BuildRuleRecord[]>(initialAccess?.build.rules ?? []);
  const [submitRules, setSubmitRules] = useState<SubmitRuleRecord[]>(initialAccess?.submit.rules ?? []);
  const [environmentRules, setEnvironmentRules] = useState<EnvironmentRuleRecord[]>(
    initialAccess?.environments.rules ?? []
  );
  const [environmentsMode, setEnvironmentsMode] = useState<'all' | 'custom'>(
    environmentRules.length === 0 ? 'all' : 'custom'
  );
  const environmentsQuery = useQuery({
    queryKey: ['environments', selectedAppId],
    queryFn: () => api.getEnvironments(),
    enabled: !!selectedAppId,
  });
  const environments = (environmentsQuery.data ?? []).map(environment => environment.name);
  const identifiersQuery = useQuery({
    queryKey: ['identifiers', selectedAppId],
    queryFn: () => api.getAppIdentifiers(),
    enabled: !!selectedAppId,
  });
  const [allowedIpsText, setAllowedIpsText] = useState(
    (initialAccess?.allowedIps ?? []).join('\n')
  );
  const [error, setError] = useState<string | null>(null);
  const [isSaving, setIsSaving] = useState(false);

  const updateRule = (index: number, patch: Partial<UpdateRuleRecord>) => {
    setRules(current => current.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));
  };

  const toggleAction = (index: number, action: UpdateAction) => {
    const rule = rules[index];
    const next = rule.actions.includes(action)
      ? rule.actions.filter(granted => granted !== action)
      : [...rule.actions, action];
    updateRule(index, { actions: next });
  };

  // Checked client-side so the operator sees the problem next to the field
  // rather than as a toast carrying the server's version of it.
  const validate = (): string | null => {
    for (const [domain, entries] of [['Build', buildRules], ['Submit', submitRules]] as const) {
      const seen = new Set<string>();
      for (const rule of entries) {
        if (!rule.appIdentifierId || !rule.actions.length) return `${domain}: choose an identifier and at least one action.`;
        const key = rule.appIdentifierId + ('destination' in rule ? ':' + rule.destination : '');
        if (seen.has(key)) return `${domain}: merge duplicate rules.`;
        seen.add(key);
      }
    }
    if (environmentsMode === 'custom') {
      if (environmentRules.length === 0) return 'Add at least one environment, or choose Every environment.';
      const seenEnvironments = new Set<string>();
      for (const rule of environmentRules) {
        const pattern = rule.pattern.trim();
        if (!pattern) return 'Every environment rule needs a name or a pattern.';
        if (pattern.includes('/') || pattern.includes('\\')) {
          return `“${pattern}” cannot contain a slash: an environment name is a single segment.`;
        }
        const collapsed = pattern.replace(/\*+/g, '*');
        if (seenEnvironments.has(collapsed)) return `“${pattern}” appears twice in Environments.`;
        seenEnvironments.add(collapsed);
      }
    }
    if (updatesMode !== 'custom') return null;
    if (rules.length === 0) return 'Add at least one rule, or choose No access.';
    const seen = new Set<string>();
    for (const rule of rules) {
      const pattern = rule.pattern.trim();
      if (!pattern) return 'Every rule needs a branch name or a pattern.';
      if (pattern.includes('/') || pattern.includes('\\')) {
        return `“${pattern}” cannot contain a slash: a branch name is a single segment.`;
      }
      // Duplicates are judged on the collapsed form, like the server: "a*"
      // and "a**" name the same set of branches.
      const collapsed = pattern.replace(/\*+/g, '*');
      if (seen.has(collapsed)) return `“${pattern}” appears twice. Merge the two rules into one.`;
      seen.add(collapsed);
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
        updates: {
          rules:
            updatesMode === 'all'
              ? [{ pattern: '*', actions: ['read', 'publish', 'rollback'] }]
              : updatesMode === 'custom'
                ? rules.map(rule => ({ ...rule, pattern: rule.pattern.trim() }))
                : [],
        },
        build: { rules: buildRules },
        submit: { rules: submitRules },
        environments: {
          rules:
            environmentsMode === 'custom'
              ? environmentRules.map(rule => ({ pattern: rule.pattern.trim() }))
              : [],
        },
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
        <p className="text-sm font-medium">Updates</p>
        <div className="grid gap-2">
          <ScopeChoice
            selected={updatesMode === 'none'}
            onSelect={() => setUpdatesMode('none')}
            title="No access"
            description="The token cannot read, publish or roll back updates."
          />
          <ScopeChoice
            selected={updatesMode === 'all'}
            onSelect={() => setUpdatesMode('all')}
            title="Every branch"
            description="The token can read, publish and roll back anywhere in this app."
          />
          <ScopeChoice
            selected={updatesMode === 'custom'}
            onSelect={() => {
              setUpdatesMode('custom');
              if (rules.length === 0) setRules([{ pattern: '', actions: ['read', 'publish'] }]);
            }}
            title="Only the branches I list"
            description="Anything not listed is refused, including branches created later."
          />
        </div>
      </div>

      {updatesMode === 'custom' && (
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
                      aria-pressed={isGranted || impliedByWrite}
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

      {identifiersQuery.isError ? (
        <div className="space-y-2 text-sm text-destructive">
          <p>Could not load app identifiers.</p>
          <Button variant="outline" onClick={() => identifiersQuery.refetch()}>Try again</Button>
        </div>
      ) : identifiersQuery.isLoading ? <Skeleton className="h-24 w-full" /> : (
        <>
          <NativeRulesEditor
            domain="Build" identifiers={identifiersQuery.data ?? []}
            rules={buildRules} onChange={setBuildRules} disabled={isSaving}
          />
          <NativeRulesEditor
            domain="Submit" identifiers={identifiersQuery.data ?? []}
            rules={submitRules} onChange={setSubmitRules} disabled={isSaving}
          />
        </>
      )}

      <div className="space-y-2">
        <p className="text-sm font-medium">Environments</p>
        <div className="grid gap-2">
          <ScopeChoice
            selected={environmentsMode === 'all'}
            onSelect={() => setEnvironmentsMode('all')}
            title="Every environment"
            description="The token can read the variables of any environment of this app."
          />
          <ScopeChoice
            selected={environmentsMode === 'custom'}
            onSelect={() => {
              setEnvironmentsMode('custom');
              if (environmentRules.length === 0) setEnvironmentRules([{ pattern: '' }]);
            }}
            title="Only the environments I list"
            description="Anything not listed is refused, including environments created later."
          />
        </div>
      </div>

      {environmentsMode === 'custom' && (
        <div className="space-y-3">
          {environmentRules.map((rule, index) => (
            <div key={index} className="flex items-start gap-2">
              <div className="min-w-0 flex-1">
                <NamePatternInput
                  value={rule.pattern}
                  onChange={pattern =>
                    setEnvironmentRules(current =>
                      current.map((entry, i) => (i === index ? { pattern } : entry))
                    )
                  }
                  names={environments}
                  noun={{ one: 'environment', many: 'environments' }}
                  placeholder="staging, or preview-*"
                  disabled={isSaving}
                />
              </div>
              <Button
                variant="ghost"
                size="icon"
                className="h-9 w-9 shrink-0 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
                title="Remove this environment"
                disabled={isSaving}
                onClick={() =>
                  setEnvironmentRules(current => current.filter((_, i) => i !== index))
                }>
                <Trash2 className="h-4 w-4" />
              </Button>
            </div>
          ))}
          <Button
            variant="outline"
            size="sm"
            disabled={isSaving}
            onClick={() => setEnvironmentRules(current => [...current, { pattern: '' }])}>
            <Plus className="mr-1.5 h-3.5 w-3.5" />
            Add an environment
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
        <Button onClick={handleSave} disabled={isSaving || identifiersQuery.isLoading || identifiersQuery.isError}>
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

const SUBMIT_DESTINATIONS: Record<string, { value: SubmitDestination; label: string }[]> = {
  android: [
    { value: 'internal', label: 'Internal testing' },
    { value: 'alpha', label: 'Alpha testing' },
    { value: 'beta', label: 'Beta testing' },
    { value: 'production', label: 'Production' },
  ],
  ios: [
    { value: 'testflight', label: 'TestFlight' },
  ],
};

type NativeEditorProps = {
  identifiers: AppIdentifier[];
  disabled: boolean;
} & (
  | { domain: 'Build'; rules: BuildRuleRecord[]; onChange: (rules: BuildRuleRecord[]) => void }
  | { domain: 'Submit'; rules: SubmitRuleRecord[]; onChange: (rules: SubmitRuleRecord[]) => void }
);

const NativeRulesEditor = (props: NativeEditorProps) => {
  const { domain, identifiers, disabled, rules } = props;
  const update = (index: number, next: BuildRuleRecord | SubmitRuleRecord) => {
    if (props.domain === 'Build') {
      props.onChange(props.rules.map((rule, i) => i === index ? next as BuildRuleRecord : rule));
    } else {
      props.onChange(props.rules.map((rule, i) => i === index ? next as SubmitRuleRecord : rule));
    }
  };
  return (
    <div className="space-y-3">
      <p className="text-sm font-medium">{domain}</p>
      <p className="text-xs text-muted-foreground">
        {domain === 'Build'
          ? 'Allow builds only for the identifiers listed, with any build profile.'
          : 'Allow uploads only for the identifiers and destinations listed.'}
        {' '}Leave empty to allow all {domain === 'Build' ? 'builds' : 'uploads'} for this app.
      </p>
      {rules.map((rule, index) => {
        const identifier = identifiers.find(item => item.id === rule.appIdentifierId);
        return (
          <div key={index} className="space-y-2 rounded-lg border p-3">
            <div className="flex gap-2">
              <select
                aria-label={`${domain} app identifier`} value={rule.appIdentifierId} disabled={disabled}
                className="min-w-0 flex-1 rounded-md border bg-background p-2 text-sm"
                onChange={event => {
                  const appIdentifierId = event.target.value;
                  if (domain === 'Build') update(index, { appIdentifierId, actions: rule.actions as BuildAction[] });
                  else {
                    const platform = identifiers.find(item => item.id === appIdentifierId)?.platform;
                    update(index, { appIdentifierId, destination: platform === 'ios' ? 'testflight' : 'internal', actions: ['upload'] });
                  }
                }}>
                <option value="" disabled>Choose an app identifier</option>
                {rule.appIdentifierId && !identifier && <option value={rule.appIdentifierId}>Unavailable identifier ({rule.appIdentifierId})</option>}
                {identifiers.map(item => <option key={item.id} value={item.id}>{item.identifier} ({item.platform})</option>)}
              </select>
              <Button variant="ghost" size="icon" title={`Remove ${domain} rule`} disabled={disabled}
                onClick={() => {
                  if (props.domain === 'Build') props.onChange(props.rules.filter((_, i) => i !== index));
                  else props.onChange(props.rules.filter((_, i) => i !== index));
                }}><Trash2 className="h-4 w-4" /></Button>
            </div>
            {'destination' in rule && (
              <select aria-label="Submit destination" value={rule.destination} disabled={disabled || !identifier}
                className="w-full rounded-md border bg-background p-2 text-sm"
                onChange={event => update(index, { ...rule, destination: event.target.value as SubmitDestination, actions: ['upload'] })}>
                {(SUBMIT_DESTINATIONS[identifier?.platform ?? ''] ?? []).map(item => <option key={item.value} value={item.value}>{item.label}</option>)}
              </select>
            )}
            <p className="text-xs text-muted-foreground">
              {domain === 'Build' ? 'Allows creating builds.' : 'Allows uploading a binary to this destination.'}
            </p>
          </div>
        );
      })}
      {!identifiers.length && <p className="text-xs text-muted-foreground">Register an app identifier in Build settings first.</p>}
      <Button variant="outline" size="sm" disabled={disabled || !identifiers.length || rules.length >= 50}
        onClick={() => {
          if (props.domain === 'Build') props.onChange([...props.rules, { appIdentifierId: '', actions: ['create'] }]);
          else props.onChange([...props.rules, { appIdentifierId: '', destination: 'internal', actions: ['upload'] }]);
        }}><Plus className="mr-1.5 h-3.5 w-3.5" />Add an identifier</Button>
    </div>
  );
};
