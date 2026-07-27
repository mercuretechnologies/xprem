// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { api, ApiKeyRecord, ApiKeyRestrictionsRecord, describeApiError } from '@/lib/api';
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

// Side panel to edit the enterprise access restrictions of one API token:
// whether it may reach protected branches at all, writing or reading, and
// which source addresses may use it. Without a valid license the form is
// masked by EnterpriseFeatureGate.
export const ApiKeyRestrictionsSheet = ({
  apiKey,
  onClose,
}: {
  apiKey: ApiKeyRecord | null;
  onClose: () => void;
}) => {
  const { selectedAppId } = useSelectedApp();

  const restrictionsQuery = useQuery({
    queryKey: ['apiKeyRestrictions', selectedAppId],
    queryFn: () => api.getApiKeyRestrictions(),
    enabled: !!selectedAppId,
  });

  return (
    <Sheet open={!!apiKey} onOpenChange={open => !open && onClose()}>
      <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-md">
        <SheetHeader>
          <SheetTitle>Access restrictions</SheetTitle>
          <SheetDescription>
            Control whether “{apiKey?.name}” can reach protected branches, and whitelist the IP
            addresses allowed to use it. Branches are protected from the Branches page.
          </SheetDescription>
        </SheetHeader>
        <div className="mt-6">
          <EnterpriseFeatureGate>
            {restrictionsQuery.isLoading ? (
              <div className="space-y-3">
                <Skeleton className="h-12 w-full" />
                <Skeleton className="h-4 w-1/2" />
                <Skeleton className="h-24 w-full" />
              </div>
            ) : restrictionsQuery.isError ? (
              <div className="space-y-3 text-sm text-muted-foreground">
                <p>Could not load this token’s restrictions.</p>
                <Button variant="outline" onClick={() => restrictionsQuery.refetch()}>
                  Try again
                </Button>
              </div>
            ) : (
              apiKey && (
                <RestrictionsForm
                  // Remount when switching tokens so the form state resets to
                  // the stored restrictions of the newly opened token.
                  key={apiKey.id}
                  apiKey={apiKey}
                  initialRestrictions={restrictionsQuery.data?.find(
                    restriction => restriction.apiKeyId === apiKey.id
                  )}
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

const RestrictionsForm = ({
  apiKey,
  initialRestrictions,
  onSaved,
}: {
  apiKey: ApiKeyRecord;
  initialRestrictions?: ApiKeyRestrictionsRecord;
  onSaved: () => void;
}) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const [canAccessProtectedBranches, setCanAccessProtectedBranches] = useState(
    initialRestrictions?.canAccessProtectedBranches ?? false
  );
  const [allowedIpsText, setAllowedIpsText] = useState(
    (initialRestrictions?.allowedIps ?? []).join('\n')
  );
  const [isSaving, setIsSaving] = useState(false);

  const handleSave = async () => {
    setIsSaving(true);
    try {
      const allowedIps = allowedIpsText
        .split('\n')
        .map(line => line.trim())
        .filter(Boolean);
      await api.setApiKeyRestrictions(apiKey.id, { canAccessProtectedBranches, allowedIps });
      queryClient.invalidateQueries({ queryKey: ['apiKeyRestrictions', selectedAppId] });
      toast({
        title: 'Restrictions saved',
        description: `“${apiKey.name}” now uses the updated restrictions.`,
      });
      onSaved();
    } catch (error) {
      const { title, description } = describeApiError(error, 'Could not save restrictions');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <p className="text-sm font-medium">Protected branches</p>
        <label className="flex cursor-pointer items-start gap-2.5 rounded-lg border px-3 py-2.5 transition-colors hover:bg-muted/40">
          <input
            type="checkbox"
            className="mt-0.5 h-4 w-4 rounded border-input accent-emerald-600"
            checked={canAccessProtectedBranches}
            onChange={event => setCanAccessProtectedBranches(event.target.checked)}
          />
          <span>
            <span className="block text-sm font-medium">Can reach protected branches</span>
            <span className="mt-0.5 block text-xs text-muted-foreground">
              Lets this token publish, roll back and republish on a protected branch, and also read
              what is on one: its runtime versions and the updates already shipped there. Unchecked,
              the token works normally everywhere else and is refused on protected branches alone,
              which is what you want for a token handed to a developer or pasted into a shared CI
              pipeline.
            </span>
          </span>
        </label>
      </div>

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
          rows={5}
          spellCheck={false}
          className="w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        />
      </div>

      <div className="flex justify-end">
        <Button onClick={handleSave} disabled={isSaving}>
          {isSaving ? 'Saving…' : 'Save restrictions'}
        </Button>
      </div>
    </div>
  );
};
