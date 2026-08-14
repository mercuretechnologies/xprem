import { useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Eye, EyeOff, Upload, X } from 'lucide-react';
import { api, ApiProblemError } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Separator } from '@/components/ui/separator';
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card';

const MAX_KEYSTORE_BYTES = 512 * 1024;

const arrayBufferToBase64 = (buffer: ArrayBuffer) => {
  const bytes = new Uint8Array(buffer);
  let binary = '';
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
};

const PasswordInput = ({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  onChange: (value: string) => void;
}) => {
  const [visible, setVisible] = useState(false);
  return (
    <div className="space-y-2">
      <Label htmlFor={id}>{label}</Label>
      <div className="relative">
        <Input
          id={id}
          type={visible ? 'text' : 'password'}
          autoComplete="off"
          value={value}
          onChange={e => onChange(e.target.value)}
          className="pr-10"
        />
        <button
          type="button"
          onClick={() => setVisible(v => !v)}
          aria-label={visible ? `Hide ${label.toLowerCase()}` : `Show ${label.toLowerCase()}`}
          className="absolute inset-y-0 right-0 flex w-10 items-center justify-center text-muted-foreground hover:text-foreground">
          {visible ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
        </button>
      </div>
    </div>
  );
};

// Hidden file input behind a button, with the picked file name shown inline.
const FilePickerRow = ({
  accept,
  fileName,
  onPick,
  onClear,
}: {
  accept: string;
  fileName: string | null;
  onPick: (file: File) => void;
  onClear: () => void;
}) => {
  const inputRef = useRef<HTMLInputElement>(null);
  return (
    <div className="flex items-center gap-2">
      <div className="flex h-9 flex-1 items-center rounded-lg border bg-muted/30 px-3 text-sm">
        {fileName ? (
          <span className="flex w-full items-center justify-between gap-2">
            <span className="truncate font-medium">{fileName}</span>
            <button
              type="button"
              onClick={onClear}
              aria-label="Remove file"
              className="text-muted-foreground hover:text-foreground">
              <X className="h-3.5 w-3.5" />
            </button>
          </span>
        ) : (
          <span className="text-muted-foreground">No file selected</span>
        )}
      </div>
      <input
        ref={inputRef}
        type="file"
        accept={accept}
        className="hidden"
        onChange={e => {
          const file = e.target.files?.[0];
          if (file) onPick(file);
          e.target.value = '';
        }}
      />
      <Button type="button" variant="outline" onClick={() => inputRef.current?.click()}>
        <Upload className="h-4 w-4" /> Choose file
      </Button>
    </div>
  );
};

type FormProps = {
  identifierId: string;
  // 'setup' is the first configuration; 'replace' overwrites an existing one
  // and offers a cancel back to the summary view.
  mode: 'setup' | 'replace';
  initialKeyAlias?: string;
  onCancel?: () => void;
  onSaved?: () => void;
};

export const AndroidCredentialsForm = ({
  identifierId,
  mode,
  initialKeyAlias = '',
  onCancel,
  onSaved,
}: FormProps) => {
  const { selectedAppId } = useSelectedApp();
  const { toast } = useToast();
  const queryClient = useQueryClient();

  const [isSaving, setIsSaving] = useState(false);
  const [keystoreFile, setKeystoreFile] = useState<File | null>(null);
  const [keystorePassword, setKeystorePassword] = useState('');
  const [keyAlias, setKeyAlias] = useState(initialKeyAlias);
  const [keyPassword, setKeyPassword] = useState('');
  const [serviceAccountKeyJson, setServiceAccountKeyJson] = useState('');
  const [serviceAccountKeyFileName, setServiceAccountKeyFileName] = useState<string | null>(null);

  const isComplete = !!keystoreFile && !!keystorePassword && !!keyAlias.trim() && !!keyPassword;

  const handlePickKeystore = (file: File) => {
    if (file.size > MAX_KEYSTORE_BYTES) {
      toast({
        title: 'Keystore too large',
        description: `The keystore exceeds the ${MAX_KEYSTORE_BYTES / 1024} KB limit.`,
        variant: 'destructive',
      });
      return;
    }
    setKeystoreFile(file);
  };

  const handlePickServiceAccountKey = async (file: File) => {
    const text = await file.text();
    try {
      JSON.parse(text);
    } catch {
      toast({
        title: 'Invalid service account key',
        description: 'The selected file is not valid JSON.',
        variant: 'destructive',
      });
      return;
    }
    setServiceAccountKeyJson(text);
    setServiceAccountKeyFileName(file.name);
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!keystoreFile || !isComplete) return;
    setIsSaving(true);
    try {
      const keystore = arrayBufferToBase64(await keystoreFile.arrayBuffer());
      await api.saveAndroidCredentials(identifierId, {
        keyAlias: keyAlias.trim(),
        keystore,
        keystorePassword,
        keyPassword,
        googleServiceAccountKey: serviceAccountKeyJson,
      });
      queryClient.invalidateQueries({ queryKey: ['identifiers', selectedAppId] });
      queryClient.invalidateQueries({ queryKey: ['androidCredentials', selectedAppId, identifierId] });
      toast({
        title: 'Credentials saved',
        description: 'The Android signing credentials are configured.',
      });
      onSaved?.();
    } catch (error) {
      let errorTitle = 'Error saving credentials';
      let errorMessage = 'An unexpected error occurred.';
      if (error instanceof ApiProblemError) {
        errorTitle = error.title;
        errorMessage = error.detail;
      } else if (error instanceof Error) {
        errorMessage = error.message;
      }
      toast({ title: errorTitle, description: errorMessage, variant: 'destructive' });
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <form onSubmit={handleSave}>
      <Card>
        <CardHeader className="border-b">
          <CardTitle className="text-base">
            {mode === 'setup' ? 'Set up Android signing' : 'Replace Android signing credentials'}
          </CardTitle>
          <CardDescription>
            {mode === 'setup'
              ? 'Provide the keystore that signs your release builds. Everything is encrypted at rest and never leaves the server.'
              : 'Saving overwrites the current keystore, passwords and service account key.'}
          </CardDescription>
        </CardHeader>

        <CardContent className="space-y-6 py-6">
          <div className="space-y-5">
            <div>
              <h3 className="text-sm font-semibold">Signing keystore</h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                A .jks or .p12 file, together with its passwords and key alias.
              </p>
            </div>
            <div className="space-y-2">
              <Label>Keystore file</Label>
              <FilePickerRow
                accept=".jks,.p12,.keystore"
                fileName={keystoreFile?.name ?? null}
                onPick={handlePickKeystore}
                onClear={() => setKeystoreFile(null)}
              />
            </div>
            <div className="grid gap-5 sm:grid-cols-2">
              <PasswordInput
                id="keystore-password"
                label="Keystore password"
                value={keystorePassword}
                onChange={setKeystorePassword}
              />
              <div className="space-y-2">
                <Label htmlFor="key-alias">Key alias</Label>
                <Input
                  id="key-alias"
                  value={keyAlias}
                  onChange={e => setKeyAlias(e.target.value)}
                />
              </div>
              <PasswordInput
                id="key-password"
                label="Key password"
                value={keyPassword}
                onChange={setKeyPassword}
              />
            </div>
          </div>

          <Separator />

          <div className="space-y-5">
            <div>
              <h3 className="text-sm font-semibold">Google Play service account</h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                Optional. A service account JSON key used to upload builds to Google Play on your
                behalf.
              </p>
            </div>
            <div className="space-y-2">
              <Label>Service account key (.json)</Label>
              <FilePickerRow
                accept=".json,application/json"
                fileName={serviceAccountKeyFileName}
                onPick={handlePickServiceAccountKey}
                onClear={() => {
                  setServiceAccountKeyJson('');
                  setServiceAccountKeyFileName(null);
                }}
              />
            </div>
          </div>
        </CardContent>

        <CardFooter className="justify-end gap-2 border-t py-4">
          {mode === 'replace' && (
            <Button type="button" variant="ghost" onClick={onCancel} disabled={isSaving}>
              Cancel
            </Button>
          )}
          <Button type="submit" disabled={isSaving || !isComplete}>
            {isSaving ? 'Saving…' : 'Save credentials'}
          </Button>
        </CardFooter>
      </Card>
    </form>
  );
};
