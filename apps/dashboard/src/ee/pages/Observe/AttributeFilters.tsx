// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router';
import { Lock, Sparkles, X } from 'lucide-react';
import { api } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Combobox } from '@/components/Combobox';
import { EnterpriseExplainerDialog } from '@/ee/components/EnterpriseExplainerDialog';
import { MultiSelect } from './MultiSelect';
import { attributePair, splitPair } from './filters';

const compact = new Intl.NumberFormat(undefined, { notation: 'compact', maximumFractionDigits: 1 });

// One row: an attribute and the values it accepts. Values load per key, so the
// autocomplete of one attribute never waits on another.
const AttributeRow = ({
  attributeKey,
  values,
  onToggle,
  onClear,
  onRemove,
}: {
  attributeKey: string;
  values: string[];
  onToggle: (value: string) => void;
  onClear: () => void;
  onRemove: () => void;
}) => {
  const schemaQuery = useQuery({
    queryKey: ['identity', 'schema', api.getAppId()],
    queryFn: () => api.getIdentitySchema(),
  });
  const spec = schemaQuery.data?.keys.find(entry => entry.key === attributeKey);
  const valuesQuery = useQuery({
    queryKey: ['identity', 'values', api.getAppId(), attributeKey],
    queryFn: () => api.searchIdentityValues(attributeKey),
    // A boolean has two values by definition, so it needs no lookup.
    enabled: Boolean(attributeKey) && spec?.type !== 'boolean',
  });

  const options =
    spec?.type === 'boolean'
      ? [
          { value: 'true', label: 'true' },
          { value: 'false', label: 'false' },
        ]
      : (valuesQuery.data?.values ?? []).map(entry => ({
          value: entry.value,
          label: `${entry.value} · ${compact.format(entry.deviceCount)} devices`,
        }));

  return (
    <div className="flex items-center gap-2">
      <span className="w-32 shrink-0 truncate font-mono text-xs" title={attributeKey}>
        {attributeKey}
      </span>
      <MultiSelect
        className="min-w-0 flex-1"
        label="Value"
        loading={valuesQuery.isLoading}
        values={values}
        onToggle={onToggle}
        onClear={onClear}
        options={options}
      />
      <Button
        type="button"
        variant="ghost"
        size="icon"
        aria-label={`Remove the ${attributeKey} filter`}
        onClick={onRemove}
        className="shrink-0 text-muted-foreground hover:text-foreground">
        <X className="h-3.5 w-3.5" />
      </Button>
    </div>
  );
};

// Several attributes narrow together ("plan is pro or enterprise, and tenant is
// globex"), so this is a list rather than a single key and a single value.
export const AttributeFilters = ({
  pairs,
  onChange,
}: {
  pairs: string[];
  onChange: (pairs: string[]) => void;
}) => {
  // A key picked but not yet given a value has no pair to store in the URL, so
  // its row lives here until it does.
  const [pending, setPending] = useState<string[]>([]);
  const [isExplainerOpen, setIsExplainerOpen] = useState(false);

  const schemaQuery = useQuery({
    queryKey: ['identity', 'schema', api.getAppId()],
    queryFn: () => api.getIdentitySchema(),
  });
  // Shares the ['license'] query with the License page, so activating a key
  // opens this section immediately.
  const licenseQuery = useQuery({ queryKey: ['license'], queryFn: () => api.getLicense() });

  // Insertion order, so a row never jumps while values are being picked.
  const grouped: Array<[string, string[]]> = [];
  for (const pair of pairs) {
    const [key, value] = splitPair(pair);
    const existing = grouped.find(entry => entry[0] === key);
    if (existing) existing[1].push(value);
    else grouped.push([key, [value]]);
  }
  for (const key of pending) {
    if (!grouped.some(entry => entry[0] === key)) grouped.push([key, []]);
  }

  const used = grouped.map(([key]) => key);
  const available = (schemaQuery.data?.keys ?? []).filter(entry => !used.includes(entry.key));

  const toggle = (key: string, value: string) => {
    const pair = attributePair(key, value);
    onChange(pairs.includes(pair) ? pairs.filter(entry => entry !== pair) : [...pairs, pair]);
  };

  // Clearing the values keeps the row, so the attribute can be given other
  // ones; the cross beside it is what drops the attribute altogether.
  const clear = (key: string) => {
    setPending(current => (current.includes(key) ? current : [...current, key]));
    onChange(pairs.filter(pair => splitPair(pair)[0] !== key));
  };

  const remove = (key: string) => {
    setPending(current => current.filter(entry => entry !== key));
    onChange(pairs.filter(pair => splitPair(pair)[0] !== key));
  };

  // An inline note rather than the frosted overlay: this section shares the
  // popover with the community filters, which must stay usable. Whatever is
  // already selected keeps its chips in the bar, so nothing silently narrows a
  // page while being unreachable here.
  const licensed = licenseQuery.data?.valid ?? false;

  return (
    <div className="space-y-2">
      <div className="flex items-baseline justify-between">
        <span className="text-[11px] font-medium text-muted-foreground">Identity attributes</span>
        {licensed && (
          <Link to="/observe/attributes" className="text-[11px] text-primary hover:underline">
            Manage attributes
          </Link>
        )}
      </div>

      {!licensed && !licenseQuery.isLoading && (
        <div className="flex items-start gap-2.5 rounded-lg border border-emerald-400/20 bg-emerald-400/[0.06] px-3 py-2.5">
          <Lock className="mt-0.5 h-3.5 w-3.5 shrink-0 text-emerald-700 dark:text-emerald-300" />
          <div className="min-w-0 space-y-1.5">
            <p className="text-[11px] leading-relaxed text-muted-foreground">
              Filtering by your own attributes (plan, tenant, user) is an Enterprise feature.
            </p>
            <Button size="sm" variant="outline" onClick={() => setIsExplainerOpen(true)}>
              <Sparkles className="h-3.5 w-3.5" />
              Discover Enterprise
            </Button>
          </div>
        </div>
      )}

      <EnterpriseExplainerDialog open={isExplainerOpen} onOpenChange={setIsExplainerOpen} />

      {licensed &&
        grouped.map(([key, values]) => (
          <AttributeRow
            key={key}
            attributeKey={key}
            values={values}
            onToggle={value => toggle(key, value)}
            onClear={() => clear(key)}
            onRemove={() => remove(key)}
          />
        ))}

      {licensed && available.length > 0 && (
        <Combobox
          className="w-full"
          label={grouped.length > 0 ? 'Add another attribute' : 'Filter on an attribute'}
          value=""
          onChange={key => setPending(current => [...current, key])}
          loading={schemaQuery.isLoading}
          options={available.map(entry => ({ value: entry.key, label: entry.key }))}
        />
      )}

      {licensed && grouped.length === 0 && available.length === 0 && !schemaQuery.isLoading && (
        <p className="text-[11px] text-muted-foreground">
          No attribute is declared yet. Attributes come from <code>$set</code> in your app.
        </p>
      )}
    </div>
  );
};
