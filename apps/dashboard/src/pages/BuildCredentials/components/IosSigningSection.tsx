import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle } from 'lucide-react';
import { ApiError } from '@/components/APIError';
import { api, AppleDistributionCertificate, describeApiError, IosSigningSetting } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { cn, formatTimestamp } from '@/lib/utils';
import { useToast } from '@/hooks/use-toast';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { IosCertificateExportGuide } from './AppleSetupGuide';
import { IosCertificateUploadForm } from './IosCertificateUploadForm';

type Mode = IosSigningSetting['mode'];

type Props = {
  identifierId: string;
  setting: IosSigningSetting;
  hasApiKey: boolean;
  canManage: boolean;
  onChanged: () => void;
};

/** Displays one signing mode with its saved-state marker and permission controls. */
const ModeOption = ({
  name,
  checked,
  isCurrent,
  disabled,
  onSelect,
  title,
  description,
}: {
  name: string;
  checked: boolean;
  isCurrent: boolean;
  disabled: boolean;
  onSelect: () => void;
  title: string;
  description: string;
}) => (
  <label
    className={cn(
      'flex items-start gap-3 rounded-lg border p-4 transition-colors',
      checked && 'border-primary bg-accent/30',
      disabled ? 'cursor-not-allowed' : 'cursor-pointer hover:bg-accent/20'
    )}>
    <input
      type="radio"
      name={name}
      checked={checked}
      onChange={onSelect}
      disabled={disabled}
      className="mt-0.5 h-4 w-4 accent-primary"
    />
    <span className="min-w-0 space-y-1">
      <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
        {title}
        {isCurrent && <Badge variant="secondary">Current</Badge>}
      </span>
      <span className="block text-xs text-muted-foreground">{description}</span>
    </span>
  </label>
);

/** Offers locally usable Apple certificates and an import form for missing private keys. */
const CertificateList = ({
  identifierId,
  name,
  certificates,
  selectedId,
  savedId,
  disabled,
  onSelect,
  onImported,
}: {
  identifierId: string;
  name: string;
  certificates: AppleDistributionCertificate[];
  selectedId: string | null;
  savedId: string | null;
  disabled: boolean;
  onSelect: (certificateId: string) => void;
  onImported: (certificates: AppleDistributionCertificate[], fingerprintSha1: string) => void;
}) => {
  const [uploadingFor, setUploadingFor] = useState<string | null>(null);

  return (
    <div className="space-y-2">
      {!certificates.some(certificate => certificate.selectable) && (
        <p className="text-sm text-muted-foreground">
          No certificate available yet. Automatic management creates one on the first submit.
        </p>
      )}
      {certificates.length > 0 && (
        <ul className="divide-y rounded-lg border">
          {certificates.map(certificate => {
            const certificateId = certificate.selectable ? certificate.xpremCertificateId : null;
            const isUploading = uploadingFor === certificate.appleId;
            return (
              <li key={certificate.appleId} className="space-y-3 px-4 py-3">
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <label
                    className={cn(
                      'flex min-w-0 items-start gap-3',
                      certificateId && !disabled ? 'cursor-pointer' : 'cursor-not-allowed',
                      !certificateId && 'opacity-60'
                    )}>
                    <input
                      type="radio"
                      name={name}
                      checked={!!certificateId && certificateId === selectedId}
                      onChange={() => certificateId && onSelect(certificateId)}
                      disabled={!certificateId || disabled}
                      className="mt-0.5 h-4 w-4 accent-primary"
                    />
                    <span className="min-w-0 space-y-0.5">
                      <span className="flex flex-wrap items-center gap-2 text-sm font-medium">
                        {certificate.name}
                        {!!certificateId && certificateId === savedId && (
                          <Badge variant="secondary">Current</Badge>
                        )}
                      </span>
                      <span className="block text-xs text-muted-foreground">
                        Expires {formatTimestamp(certificate.expiresAt)} · Serial{' '}
                        {certificate.serialNumber}
                      </span>
                    </span>
                  </label>
                  {!certificateId && !isUploading && (
                    <div className="flex items-center gap-3">
                      <span className="text-xs text-muted-foreground">Created outside xprem</span>
                      <Button
                        variant="outline"
                        size="sm"
                        onClick={() => setUploadingFor(certificate.appleId)}
                        disabled={disabled}>
                        Upload its .p12
                      </Button>
                    </div>
                  )}
                </div>
                {!certificateId && isUploading && (
                  <div className="space-y-3">
                    <IosCertificateExportGuide />
                    <IosCertificateUploadForm
                      identifierId={identifierId}
                      fingerprintSha1={certificate.fingerprintSha1}
                      onImported={updated => {
                        setUploadingFor(null);
                        onImported(updated, certificate.fingerprintSha1);
                      }}
                      onCancel={() => setUploadingFor(null)}
                    />
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
};

/** Edits automatic or selected-certificate signing for the identifier. */
export const IosSigningSection = ({
  identifierId,
  setting,
  hasApiKey,
  canManage,
  onChanged,
}: Props) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();
  const savedCertificateId = setting.certificate?.id ?? null;
  const [mode, setMode] = useState<Mode>(setting.mode);
  const [certificateId, setCertificateId] = useState<string | null>(savedCertificateId);
  const [isSaving, setIsSaving] = useState(false);

  const certificatesQuery = useQuery({
    queryKey: ['appleDistributionCertificates', selectedAppId, identifierId],
    queryFn: () => api.getAppleDistributionCertificates(identifierId),
    enabled: !!selectedAppId && hasApiKey && canManage && mode === 'certificate',
  });

  const isDirty =
    mode !== setting.mode || (mode === 'certificate' && certificateId !== savedCertificateId);
  const canSave = isDirty && (mode === 'automatic' || !!certificateId);

  /** Persists the selected signing mode and refreshes the saved credential state. */
  const handleSave = async () => {
    if (!canSave) return;
    setIsSaving(true);
    try {
      await api.updateIosSigning(
        identifierId,
        mode === 'certificate' && certificateId
          ? { mode: 'certificate', certificateId }
          : { mode: 'automatic' }
      );
      onChanged();
      toast({
        title: 'Signing certificate saved',
        description:
          mode === 'automatic'
            ? 'Automatic management'
            : certificatesQuery.data?.find(
                certificate => certificate.xpremCertificateId === certificateId
              )?.name,
      });
    } catch (error) {
      const message = describeApiError(error, 'Error saving signing settings');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsSaving(false);
    }
  };

  /** Updates the certificate cache after a matching private key is imported. */
  const handleImported = (
    certificates: AppleDistributionCertificate[],
    fingerprintSha1: string
  ) => {
    queryClient.setQueryData(
      ['appleDistributionCertificates', selectedAppId, identifierId],
      certificates
    );
    const imported = certificates.find(
      certificate => certificate.fingerprintSha1 === fingerprintSha1
    );
    if (imported?.selectable && imported.xpremCertificateId) {
      setCertificateId(imported.xpremCertificateId);
    }
  };

  /** Renders the Apple certificate loading, failure, empty or selection state. */
  const renderCertificates = () => {
    if (!canManage) return null;
    if (certificatesQuery.isPending) return <Skeleton className="h-16 w-full rounded-lg" />;
    if (certificatesQuery.isError) {
      return (
        <ApiError
          error={certificatesQuery.error}
          onRetry={() => void certificatesQuery.refetch()}
        />
      );
    }
    return (
      <CertificateList
        identifierId={identifierId}
        name="ios-signing-certificate"
        certificates={certificatesQuery.data}
        selectedId={certificateId}
        savedId={savedCertificateId}
        disabled={isSaving}
        onSelect={setCertificateId}
        onImported={handleImported}
      />
    );
  };

  return (
    <Card>
      <CardHeader className="space-y-1 border-b py-4">
        <CardTitle className="text-base">Signing certificate</CardTitle>
        <p className="text-sm text-muted-foreground">
          Used for App Store, TestFlight and Ad Hoc builds.
        </p>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        {!hasApiKey ? (
          <p className="text-sm text-muted-foreground">Connect App Store Connect first.</p>
        ) : (
          <>
            {setting.certificateMissing && (
              <p className="flex items-center gap-1.5 text-xs font-medium text-amber-700 dark:text-amber-400">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                The selected certificate no longer exists. Select another one or switch to automatic
                management.
              </p>
            )}

            <div
              role="radiogroup"
              aria-label="Signing certificate"
              className="grid gap-3 sm:grid-cols-2">
              <ModeOption
                name="ios-signing-mode"
                checked={mode === 'automatic'}
                isCurrent={setting.mode === 'automatic'}
                disabled={!canManage || isSaving}
                onSelect={() => setMode('automatic')}
                title="Automatic management"
                description="Nothing to do. xprem creates the certificate and profiles when you submit a build."
              />
              <ModeOption
                name="ios-signing-mode"
                checked={mode === 'certificate'}
                isCurrent={setting.mode === 'certificate'}
                disabled={!canManage || isSaving}
                onSelect={() => setMode('certificate')}
                title="Select a certificate"
                description="Use a distribution certificate from your Apple account. Certificates created outside xprem need their .p12."
              />
            </div>

            {mode === 'certificate' && (
              <div className="space-y-3">
                {setting.certificate && (
                  <p className="text-sm">
                    In use: <span className="font-medium">{setting.certificate.commonName}</span>
                    <span className="text-muted-foreground">
                      {' '}
                      · expires {formatTimestamp(setting.certificate.expiresAt)}
                    </span>
                  </p>
                )}
                {renderCertificates()}
              </div>
            )}

            {canManage && isDirty && (
              <div className="flex justify-end gap-2">
                <Button
                  variant="ghost"
                  onClick={() => {
                    setMode(setting.mode);
                    setCertificateId(savedCertificateId);
                  }}
                  disabled={isSaving}>
                  Cancel
                </Button>
                <Button onClick={() => void handleSave()} disabled={!canSave || isSaving}>
                  {isSaving ? 'Saving…' : 'Save'}
                </Button>
              </div>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
};
