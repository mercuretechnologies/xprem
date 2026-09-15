import { useId, useState } from 'react';
import { api, AppleDistributionCertificate, describeApiError } from '@/lib/api';
import { arrayBufferToBase64 } from '@/lib/utils';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Label } from '@/components/ui/label';
import { FilePickerRow, PasswordInput } from './AndroidCredentialsForm';

const MAX_CERTIFICATE_BYTES = 64 * 1024;

type Props = {
  identifierId: string;
  // The Apple certificate the .p12 must hold.
  fingerprintSha1: string;
  onImported: (certificates: AppleDistributionCertificate[]) => void;
  onCancel: () => void;
};

/** Imports the private key for the selected Apple distribution certificate. */
export const IosCertificateUploadForm = ({
  identifierId,
  fingerprintSha1,
  onImported,
  onCancel,
}: Props) => {
  const { toast } = useToast();
  const [certificateFile, setCertificateFile] = useState<File | null>(null);
  const [certificatePassword, setCertificatePassword] = useState('');
  const [isSaving, setIsSaving] = useState(false);
  const passwordId = useId();

  /** Selects a PKCS#12 file only when it fits the certificate upload size limit. */
  const handlePick = (file: File) => {
    if (file.size > MAX_CERTIFICATE_BYTES) {
      toast({
        title: 'Certificate too large',
        description: `The certificate exceeds the ${MAX_CERTIFICATE_BYTES / 1024} KB limit.`,
        variant: 'destructive',
      });
      return;
    }
    setCertificateFile(file);
  };

  /** Encodes the PKCS#12 identity, uploads it and reports the refreshed certificate list. */
  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (isSaving || !certificateFile) return;
    setIsSaving(true);
    try {
      const certificateP12 = arrayBufferToBase64(await certificateFile.arrayBuffer());
      const certificates = await api.importIosCertificate(identifierId, {
        fingerprintSha1,
        certificateP12,
        certificatePassword,
      });
      toast({ title: 'Certificate unlocked' });
      onImported(certificates);
    } catch (error) {
      const message = describeApiError(error, 'Error uploading certificate');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <form onSubmit={handleSave} className="space-y-4 text-foreground">
      <div className="space-y-2">
        <Label>Certificate file</Label>
        <FilePickerRow
          accept=".p12,application/x-pkcs12"
          fileName={certificateFile?.name ?? null}
          onPick={handlePick}
          onClear={() => setCertificateFile(null)}
          disabled={isSaving}
        />
      </div>
      <div className="sm:w-1/2">
        <PasswordInput
          id={passwordId}
          label="Certificate password"
          value={certificatePassword}
          onChange={setCertificatePassword}
        />
      </div>
      <div className="flex justify-end gap-2">
        <Button type="button" variant="ghost" onClick={onCancel} disabled={isSaving}>
          Cancel
        </Button>
        <Button type="submit" disabled={!certificateFile || isSaving}>
          {isSaving ? 'Uploading…' : 'Upload'}
        </Button>
      </div>
    </form>
  );
};
