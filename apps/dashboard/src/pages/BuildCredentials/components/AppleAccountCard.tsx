import { useState } from 'react';
import { CheckCircle2, Pencil, Trash2 } from 'lucide-react';
import { ApiError } from '@/components/APIError';
import { api, AppleApiKey, describeApiError } from '@/lib/api';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { DeleteDialog } from '@/components/ui/delete-dialog';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { FilePickerRow } from './AndroidCredentialsForm';
import { AppStoreConnectApiKeyGuide } from './AppleSetupGuide';

const MAX_PRIVATE_KEY_BYTES = 8 * 1024;

type Props = {
  apiKey: AppleApiKey | null;
  apiKeyError: Error | null;
  onRetry: () => void;
  canManage: boolean;
  onChanged: () => void;
};

/** Displays the team API key status and permission-gated replacement and deletion forms. */
export const AppleAccountCard = ({ apiKey, apiKeyError, onRetry, canManage, onChanged }: Props) => {
  const { toast } = useToast();
  const [keyId, setKeyId] = useState('');
  const [issuerId, setIssuerId] = useState('');
  const [privateKey, setPrivateKey] = useState('');
  const [fileName, setFileName] = useState<string | null>(null);
  const [isChanging, setIsChanging] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [isDeleting, setIsDeleting] = useState(false);
  const [isDeleteDialogOpen, setIsDeleteDialogOpen] = useState(false);
  const isComplete = !!keyId.trim() && !!issuerId.trim() && !!privateKey;

  /** Clears the private key and account identifiers from local form state. */
  const clearForm = () => {
    setKeyId('');
    setIssuerId('');
    setPrivateKey('');
    setFileName(null);
  };

  /** Reads a selected .p8 file after enforcing the private-key size limit. */
  const handlePick = async (file: File) => {
    if (file.size > MAX_PRIVATE_KEY_BYTES) {
      toast({
        title: 'Private key too large',
        description: `The .p8 file exceeds the ${MAX_PRIVATE_KEY_BYTES / 1024} KB limit.`,
        variant: 'destructive',
      });
      return;
    }
    try {
      setPrivateKey(await file.text());
      setFileName(file.name);
    } catch {
      toast({
        title: 'Invalid private key',
        description: 'The selected file could not be read.',
        variant: 'destructive',
      });
    }
  };

  /** Saves a complete API key and clears its private material after success. */
  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (isSaving || !isComplete) return;
    setIsSaving(true);
    try {
      await api.saveAppleApiKey({ keyId: keyId.trim(), issuerId: issuerId.trim(), privateKey });
      onChanged();
      clearForm();
      setIsChanging(false);
      toast({ title: 'App Store Connect connected' });
    } catch (error) {
      const message = describeApiError(error, 'Error saving API key');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsSaving(false);
    }
  };

  /** Removes the team key and refreshes the account status after confirmation. */
  const handleDelete = async () => {
    setIsDeleting(true);
    try {
      await api.deleteAppleApiKey();
      onChanged();
      setIsDeleteDialogOpen(false);
      setIsChanging(false);
      clearForm();
      toast({
        title: 'API key removed',
        description: 'Stored certificates and profiles were not changed.',
      });
    } catch (error) {
      const message = describeApiError(error, 'Error removing API key');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsDeleting(false);
    }
  };

  const form = (
    <form onSubmit={handleSave} className="space-y-4">
      <AppStoreConnectApiKeyGuide />
      <div className="grid gap-5 sm:grid-cols-2">
        <div className="space-y-2">
          <Label htmlFor="apple-key-id">Key ID</Label>
          <Input
            id="apple-key-id"
            autoComplete="off"
            value={keyId}
            onChange={e => setKeyId(e.target.value)}
          />
        </div>
        <div className="space-y-2">
          <Label htmlFor="apple-issuer-id">Issuer ID</Label>
          <Input
            id="apple-issuer-id"
            autoComplete="off"
            value={issuerId}
            onChange={e => setIssuerId(e.target.value)}
          />
        </div>
      </div>
      <div className="space-y-2">
        <Label>Private key (.p8)</Label>
        <FilePickerRow
          accept=".p8"
          fileName={fileName}
          onPick={handlePick}
          onClear={() => {
            setPrivateKey('');
            setFileName(null);
          }}
        />
      </div>
      <div className="flex justify-end gap-2">
        {isChanging && (
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              setIsChanging(false);
              clearForm();
            }}>
            Cancel
          </Button>
        )}
        <Button type="submit" disabled={!isComplete || isSaving}>
          {isSaving ? 'Connecting…' : 'Connect'}
        </Button>
      </div>
    </form>
  );

  /** Chooses the loading error, key metadata or connection form for the card. */
  const renderContent = () => {
    if (apiKeyError) {
      return <ApiError error={apiKeyError} onRetry={onRetry} />;
    }
    if (apiKey && !isChanging) {
      return (
        <div className="flex flex-wrap items-center justify-between gap-2">
          <p className="flex items-center gap-2 text-sm">
            <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-600 dark:text-emerald-400" />
            <span>
              Connected to App Store Connect (key{' '}
              <span className="font-mono text-xs">{apiKey.keyId}</span>)
            </span>
          </p>
          {canManage && (
            <div className="flex gap-2">
              <Button variant="outline" size="sm" onClick={() => setIsChanging(true)}>
                <Pencil className="h-3.5 w-3.5" /> Change
              </Button>
              <Button
                variant="ghost"
                size="sm"
                onClick={() => setIsDeleteDialogOpen(true)}
                className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive">
                <Trash2 className="h-3.5 w-3.5" /> Remove
              </Button>
            </div>
          )}
        </div>
      );
    }
    if (apiKey && isChanging) {
      return form;
    }
    if (!canManage) {
      return <p className="text-sm text-muted-foreground">Not connected to App Store Connect.</p>;
    }
    return (
      <div className="space-y-4">
        <p className="text-sm text-muted-foreground">
          Connect App Store Connect so xprem creates and renews certificates and profiles for you.
        </p>
        {form}
      </div>
    );
  };

  return (
    <>
      <Card>
        <CardHeader className="border-b py-4">
          <CardTitle className="text-base">Apple account</CardTitle>
        </CardHeader>
        <CardContent className="pt-4">{renderContent()}</CardContent>
      </Card>

      <DeleteDialog
        isOpen={isDeleteDialogOpen}
        onClose={() => setIsDeleteDialogOpen(false)}
        onConfirm={handleDelete}
        isDeleting={isDeleting}
        title="Remove App Store Connect API key"
        resourceName={apiKey ? `API key ${apiKey.keyId}` : undefined}
        descriptionText="The API key will be permanently removed from this app. Certificates and profiles already stored will not be changed."
        confirmButtonText="Remove key"
        isDeletingButtonText="Removing…"
      />
    </>
  );
};
