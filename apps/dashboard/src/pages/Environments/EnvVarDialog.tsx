import { useEffect, useState } from 'react';
import { api, describeApiError, EnvVarRecord } from '@/lib/api';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Switch } from '@/components/ui/switch';
import { Textarea } from '@/components/ui/textarea';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';

const PUBLIC_PREFIX = 'EXPO_PUBLIC_';

// Create a variable, or overwrite an existing one (key locked, value always
// re-entered: the stored value is never pulled back into a form).
export const EnvVarDialog = ({
  environmentName,
  existingKeys,
  existing,
  isOpen,
  onClose,
  onSaved,
}: {
  environmentName: string;
  existingKeys: string[];
  existing: EnvVarRecord | null;
  isOpen: boolean;
  onClose: () => void;
  onSaved: () => Promise<void>;
}) => {
  const { toast } = useToast();
  const [key, setKey] = useState('');
  const [value, setValue] = useState('');
  const [isPublic, setIsPublic] = useState(false);
  const [isSaving, setIsSaving] = useState(false);

  useEffect(() => {
    if (!isOpen) return;
    setKey(existing?.key ?? '');
    setValue('');
    setIsPublic(existing?.isPublic ?? false);
  }, [isOpen, existing]);

  const trimmedKey = key.trim();
  const keyLooksPrefixed = trimmedKey.toUpperCase().startsWith(PUBLIC_PREFIX);
  // The route is an upsert: adding a key that exists replaces its value.
  const replacesExisting = !!existing || existingKeys.includes(trimmedKey);
  // Empty values are legal, but in update mode an untouched form must not wipe
  // the current value with one click.
  const canSubmit = !!trimmedKey && !keyLooksPrefixed && (!existing || value !== '');

  const handleClose = () => {
    if (isSaving) return;
    onClose();
  };

  const handleSave = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!trimmedKey) return;
    setIsSaving(true);
    try {
      await api.setEnvVar(environmentName, trimmedKey, { value, isPublic });
      await onSaved();
      toast({
        title: replacesExisting ? 'Variable updated' : 'Variable added',
        description: `"${trimmedKey}" is set on "${environmentName}".`,
      });
      onClose();
    } catch (error) {
      const message = describeApiError(error, 'Could not save variable');
      toast({ title: message.title, description: message.description, variant: 'destructive' });
    } finally {
      setIsSaving(false);
    }
  };

  return (
    <Dialog open={isOpen} onOpenChange={open => !open && handleClose()}>
      <DialogContent className="sm:max-w-[520px]">
        <form onSubmit={handleSave}>
          <DialogHeader>
            <DialogTitle>{existing ? `Update ${existing.key}` : 'New variable'}</DialogTitle>
            <DialogDescription>
              {existing
                ? 'The new value replaces the current one. The current value is not shown here.'
                : `Values are encrypted at rest and injected into builds configured with "${environmentName}".`}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="env-var-key">Key</Label>
              <Input
                id="env-var-key"
                className="font-mono"
                placeholder="API_URL"
                value={key}
                onChange={e => setKey(e.target.value)}
                disabled={isSaving || !!existing}
                autoFocus={!existing}
                autoCapitalize="characters"
                spellCheck={false}
              />
              {keyLooksPrefixed && (
                <p className="text-xs text-amber-600 dark:text-amber-500">
                  Leave out the {PUBLIC_PREFIX} prefix: it is added automatically when the variable
                  is public.
                </p>
              )}
              {!existing && replacesExisting && (
                <p className="text-xs text-amber-600 dark:text-amber-500">
                  "{trimmedKey}" already exists in this environment: saving replaces its value.
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="env-var-value">Value</Label>
              <Textarea
                id="env-var-value"
                className="font-mono"
                value={value}
                onChange={e => setValue(e.target.value)}
                disabled={isSaving}
                autoFocus={!!existing}
                spellCheck={false}
                rows={3}
              />
              {existing && value === '' && (
                <p className="text-xs text-muted-foreground">
                  Enter the new value. The current one is kept until you save.
                </p>
              )}
            </div>

            <div className="flex items-start justify-between gap-4 rounded-lg border p-3">
              <div className="space-y-0.5">
                <Label>Public</Label>
                <p className="text-xs text-muted-foreground">
                  Exposed to the app bundle as{' '}
                  <code className="font-mono">
                    {PUBLIC_PREFIX}
                    {trimmedKey || 'KEY'}
                  </code>
                  . Leave off for secrets only the build needs.
                </p>
              </div>
              <Switch
                checked={isPublic}
                onCheckedChange={setIsPublic}
                disabled={isSaving}
                aria-label="Public variable"
              />
            </div>
          </div>

          <DialogFooter className="gap-2 border-t pt-3 sm:gap-0">
            <Button type="button" variant="outline" onClick={handleClose} disabled={isSaving}>
              Cancel
            </Button>
            <Button type="submit" disabled={isSaving || !canSubmit}>
              {isSaving ? 'Saving…' : replacesExisting ? 'Replace value' : 'Add variable'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
};
