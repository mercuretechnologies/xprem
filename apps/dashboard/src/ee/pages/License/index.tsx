// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE at the repository root); it is NOT covered by the MIT
// license of this repository.

import { useRef, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { BadgeCheck, FileUp, ShieldAlert, TriangleAlert } from 'lucide-react';
import { api, describeApiError, type LicenseCheckResult } from '@/lib/api';
import { formatTimestamp } from '@/lib/utils';
import { useSettings } from '@/lib/SettingsContext';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { useToast } from '@/hooks/use-toast';
import { licenseErrorMessage } from '@/ee/lib/licenseErrors';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { PageHeader } from '@/components/PageHeader';
import { Skeleton } from '@/components/ui/skeleton';
import { TimestampCell } from '@/components/ui/timestamp-cell';

const StatusRow = ({ label, children }: { label: string; children: React.ReactNode }) => (
  <div className="flex items-center justify-between gap-4 py-2 text-sm">
    <span className="text-muted-foreground">{label}</span>
    <span>{children}</span>
  </div>
);

export const License = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { isAdmin } = useCurrentUser();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const [keyInput, setKeyInput] = useState('');
  const [isChecking, setIsChecking] = useState(false);
  const [checkError, setCheckError] = useState<string | null>(null);
  // A valid check result awaiting the admin's confirmation in the dialog; the
  // key is frozen at check time so later edits to the textarea cannot attach
  // an unchecked key.
  const [pendingActivation, setPendingActivation] = useState<
    (LicenseCheckResult & { key: string }) | null
  >(null);
  const [isActivating, setIsActivating] = useState(false);
  const [isRemoveDialogOpen, setIsRemoveDialogOpen] = useState(false);
  const [isRemoving, setIsRemoving] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const licenseQuery = useQuery({
    queryKey: ['license'],
    queryFn: () => api.getLicense(),
    enabled: CONTROL_PLANE_ENABLED,
  });

  const notifyError = (error: unknown, fallbackTitle: string) =>
    toast({ ...describeApiError(error, fallbackTitle), variant: 'destructive' });

  const handleVerify = async () => {
    const key = keyInput.trim();
    if (!key) return;
    setIsChecking(true);
    setCheckError(null);
    try {
      const result = await api.checkLicense(key);
      if (result.valid) {
        setPendingActivation({ ...result, key });
      } else {
        setCheckError(licenseErrorMessage(result.errorCode));
      }
    } catch (error) {
      notifyError(error, 'License check failed');
    } finally {
      setIsChecking(false);
    }
  };

  const handleConfirmActivation = async () => {
    if (!pendingActivation) return;
    setIsActivating(true);
    try {
      const status = await api.activateLicense(pendingActivation.key);
      queryClient.invalidateQueries({ queryKey: ['license'] });
      // rbacEnabled is derived server-side from the license.
      queryClient.invalidateQueries({ queryKey: ['me', 'permissions'] });
      setPendingActivation(null);
      setKeyInput('');
      toast({
        title: 'License activated',
        description: `Enterprise edition is enabled for ${status.orgName ?? 'your organization'}.`,
      });
    } catch (error) {
      notifyError(error, 'License activation failed');
    } finally {
      setIsActivating(false);
    }
  };

  const handleImportFile = async (file: File | undefined) => {
    if (!file) return;
    setKeyInput((await file.text()).trim());
    setCheckError(null);
    // Reset so picking the same file again still fires onChange.
    if (fileInputRef.current) fileInputRef.current.value = '';
  };

  const handleRemove = async () => {
    setIsRemoving(true);
    try {
      await api.removeLicense();
      queryClient.invalidateQueries({ queryKey: ['license'] });
      queryClient.invalidateQueries({ queryKey: ['me', 'permissions'] });
      setIsRemoveDialogOpen(false);
      toast({
        title: 'License removed',
        description: 'This deployment is back to community edition.',
      });
    } catch (error) {
      notifyError(error, 'License removal failed');
    } finally {
      setIsRemoving(false);
    }
  };

  if (!CONTROL_PLANE_ENABLED) {
    return (
      <div className="w-full">
        <PageHeader title="License" description="Enterprise Edition license for this deployment." />
        <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
          Enterprise licenses are stored in the database and require control-plane (DB) mode.
          Stateless deployments run the community edition.
        </div>
      </div>
    );
  }

  const license = licenseQuery.data;
  const isFailingValidation = Boolean(license?.hasKey && license.validationFailedAt);

  const activationForm = (
    <div className="space-y-3">
      <textarea
        value={keyInput}
        onChange={event => {
          setKeyInput(event.target.value);
          setCheckError(null);
        }}
        placeholder="XPREM-…"
        rows={4}
        spellCheck={false}
        className="w-full resize-y rounded-md border border-input bg-transparent px-3 py-2 font-mono text-xs shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
      />
      {checkError && (
        <Alert variant="destructive">
          <ShieldAlert className="h-4 w-4" />
          <AlertTitle>This key cannot be activated</AlertTitle>
          <AlertDescription>{checkError}</AlertDescription>
        </Alert>
      )}
      <div className="flex items-center gap-2">
        <Button onClick={handleVerify} disabled={!keyInput.trim() || isChecking}>
          {isChecking ? 'Verifying…' : 'Verify key'}
        </Button>
        <Button variant="outline" onClick={() => fileInputRef.current?.click()}>
          <FileUp className="h-4 w-4" />
          Import from file
        </Button>
        <input
          ref={fileInputRef}
          type="file"
          accept=".txt,.key,.lic,text/plain"
          className="hidden"
          onChange={event => handleImportFile(event.target.files?.[0])}
        />
      </div>
    </div>
  );

  return (
    <div className="w-full">
      <PageHeader
        title="License"
        description="Enterprise Edition license for this deployment. Keys are verified against the Mercure Technologies license server and re-checked periodically."
      />

      {licenseQuery.isLoading && (
        <Card>
          <CardHeader>
            <Skeleton className="h-5 w-40" />
          </CardHeader>
          <CardContent className="space-y-2">
            <Skeleton className="h-4 w-full" />
            <Skeleton className="h-4 w-2/3" />
          </CardContent>
        </Card>
      )}

      {licenseQuery.isError && (
        <Alert variant="destructive">
          <TriangleAlert className="h-4 w-4" />
          <AlertTitle>License status unavailable</AlertTitle>
          <AlertDescription>
            The license status could not be loaded. This does not affect the license itself; refresh
            the page or try again later.
          </AlertDescription>
        </Alert>
      )}

      {!licenseQuery.isLoading && !licenseQuery.isError && (
        <div className="space-y-6">
          {license?.suspended && (
            <Alert variant="destructive">
              <TriangleAlert className="h-4 w-4" />
              <AlertTitle>Enterprise features are disabled</AlertTitle>
              <AlertDescription>
                {licenseErrorMessage(license.validationErrorCode)} Verification had been failing
                since{' '}
                {license.validationFailedAt
                  ? formatTimestamp(license.validationFailedAt)
                  : 'recently'}{' '}
                and the grace period has ended. Contact{' '}
                <a className="font-medium underline" href="mailto:support@xprem.dev">
                  support@xprem.dev
                </a>{' '}
                to restore it.
              </AlertDescription>
            </Alert>
          )}
          {isFailingValidation && !license?.suspended && (
            <Alert variant="destructive">
              <TriangleAlert className="h-4 w-4" />
              <AlertTitle>We can&apos;t verify your license</AlertTitle>
              <AlertDescription>
                {licenseErrorMessage(license?.validationErrorCode)} Verification has been failing
                since{' '}
                {license?.validationFailedAt
                  ? formatTimestamp(license.validationFailedAt)
                  : 'recently'}
                . Enterprise features stay enabled until{' '}
                {license?.graceEndsAt ? formatTimestamp(license.graceEndsAt) : 'soon'}; contact{' '}
                <a className="font-medium underline" href="mailto:support@xprem.dev">
                  support@xprem.dev
                </a>{' '}
                as soon as possible.
              </AlertDescription>
            </Alert>
          )}

          <Card>
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                {license?.valid ? (
                  <>
                    <BadgeCheck className="h-5 w-5 text-emerald-700 dark:text-emerald-300" />
                    Enterprise edition
                    <Badge>Active</Badge>
                  </>
                ) : license?.hasKey ? (
                  <>
                    Enterprise edition
                    <Badge variant="destructive">Suspended</Badge>
                  </>
                ) : (
                  <>
                    Community edition
                    <Badge variant="secondary">No active license</Badge>
                  </>
                )}
              </CardTitle>
            </CardHeader>
            {license?.hasKey && (
              <CardContent>
                <div className="divide-y">
                  <StatusRow label="Organization">
                    <span className="font-medium">{license.orgName || '—'}</span>
                  </StatusRow>
                  <StatusRow label="Plan">
                    <Badge variant="secondary" className="capitalize">
                      {license.planCode || 'unknown'}
                    </Badge>
                  </StatusRow>
                  <StatusRow label="Subscription started">
                    <TimestampCell dateString={license.subscriptionStartAt ?? null} />
                  </StatusRow>
                  <StatusRow label="Subscription ends">
                    {license.subscriptionEndAt ? (
                      <TimestampCell dateString={license.subscriptionEndAt} />
                    ) : (
                      <span className="text-muted-foreground">No end date</span>
                    )}
                  </StatusRow>
                  {license.subscriptionRenewalAt && (
                    <StatusRow label="Renews">
                      <TimestampCell dateString={license.subscriptionRenewalAt} />
                    </StatusRow>
                  )}
                  <StatusRow label="Activated">
                    <TimestampCell dateString={license.activatedAt ?? null} />
                  </StatusRow>
                  <StatusRow label="Last verified">
                    <TimestampCell dateString={license.lastValidatedAt ?? null} />
                  </StatusRow>
                </div>
                {isAdmin && (
                  <div className="mt-4 flex justify-end">
                    <Button variant="outline" onClick={() => setIsRemoveDialogOpen(true)}>
                      Remove license
                    </Button>
                  </div>
                )}
              </CardContent>
            )}
          </Card>

          {isAdmin ? (
            <Card>
              <CardHeader>
                <CardTitle>
                  {license?.hasKey ? 'Replace license key' : 'Activate a license key'}
                </CardTitle>
                <CardDescription>
                  Paste the license key you received with your Enterprise subscription, or import
                  the file it was delivered in. The key is verified with the license server before
                  anything is activated.
                </CardDescription>
              </CardHeader>
              <CardContent>{activationForm}</CardContent>
            </Card>
          ) : (
            <div className="rounded-xl border border-dashed bg-muted/30 p-6 text-center text-sm text-muted-foreground">
              Only admins can activate or remove a license key.
            </div>
          )}
        </div>
      )}

      <Dialog
        open={pendingActivation !== null}
        onOpenChange={open => {
          if (!open) setPendingActivation(null);
        }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Activate this license?</DialogTitle>
            <DialogDescription>
              You are about to activate an Enterprise license for{' '}
              <span className="font-semibold text-foreground">
                {pendingActivation?.orgName ?? 'an organization'}
              </span>{' '}
              ({pendingActivation?.planCode ?? 'unknown'} plan) on this server.
            </DialogDescription>
          </DialogHeader>
          <Alert>
            <ShieldAlert className="h-4 w-4" />
            <AlertTitle>Make sure this license is yours</AlertTitle>
            <AlertDescription>
              Only activate a license issued to your organization. A key can only be attached to one
              server: activating a license that does not belong to you burns its activation for its
              rightful owner.
            </AlertDescription>
          </Alert>
          <DialogFooter>
            <Button variant="outline" onClick={() => setPendingActivation(null)}>
              Cancel
            </Button>
            <Button onClick={handleConfirmActivation} disabled={isActivating}>
              {isActivating
                ? 'Activating…'
                : `Activate for ${pendingActivation?.orgName ?? 'this organization'}`}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <DeleteDialog
        isOpen={isRemoveDialogOpen}
        onClose={() => setIsRemoveDialogOpen(false)}
        onConfirm={handleRemove}
        isDeleting={isRemoving}
        title="Remove license"
        resourceName={license?.orgName}
        descriptionText="Enterprise features will be disabled and this deployment drops back to community edition. The key stays attached to this server on the license server; contact support@xprem.dev if you need it released."
        confirmButtonText="Remove license"
        isDeletingButtonText="Removing…"
      />
    </div>
  );
};
