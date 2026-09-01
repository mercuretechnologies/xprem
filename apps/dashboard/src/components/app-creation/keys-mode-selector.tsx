import { KeysMode } from '@/lib/api';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { SelectableCard } from './selectable-card';

type KeysModeSelectorProps = {
  keysMode: KeysMode;
  setKeysMode: (mode: KeysMode) => void;
  publicSecretId: string;
  setPublicSecretId: (v: string) => void;
  privateSecretId: string;
  setPrivateSecretId: (v: string) => void;
  disabled: boolean;
};

const KEYS_MODES: { id: KeysMode; label: string; desc: string }[] = [
  {
    id: 'database',
    label: 'Managed for you',
    desc: 'Keys are generated, sealed with the master key and stored in the database.',
  },
  {
    id: 'aws-secrets-manager',
    label: 'AWS Secrets Manager',
    desc: 'Keys are fetched from secrets you manage in AWS.',
  },
];

export const KeysModeSelector = ({
  keysMode,
  setKeysMode,
  publicSecretId,
  setPublicSecretId,
  privateSecretId,
  setPrivateSecretId,
  disabled,
}: KeysModeSelectorProps) => (
  <>
    <div className="space-y-2">
      <Label>Signing keys</Label>
      <div className="grid grid-cols-1 gap-2">
        {KEYS_MODES.map(mode => (
          <SelectableCard
            key={mode.id}
            control="radio"
            name="keysMode"
            value={mode.id}
            selected={keysMode === mode.id}
            onSelect={() => setKeysMode(mode.id)}
            disabled={disabled}>
            <span className="text-sm font-medium text-foreground">{mode.label}</span>
            <span className="text-xs text-muted-foreground">{mode.desc}</span>
          </SelectableCard>
        ))}
      </div>
    </div>

    {keysMode === 'aws-secrets-manager' && (
      <div className="space-y-3 p-3 rounded-lg border border-dashed border-border bg-muted/20 animate-in fade-in-50 duration-200">
        <div className="space-y-1.5">
          <Label htmlFor="publicSecretId" className="text-xs font-medium text-foreground">
            AWS Secret ID (Public Key)
          </Label>
          <Input
            id="publicSecretId"
            placeholder="arn:aws:secretsmanager:..."
            value={publicSecretId}
            onChange={e => setPublicSecretId(e.target.value)}
            disabled={disabled}
            className="h-9 bg-background"
            required
          />
        </div>
        <div className="space-y-1.5">
          <Label htmlFor="privateSecretId" className="text-xs font-medium text-foreground">
            AWS Secret ID (Private Key)
          </Label>
          <Input
            id="privateSecretId"
            placeholder="arn:aws:secretsmanager:..."
            value={privateSecretId}
            onChange={e => setPrivateSecretId(e.target.value)}
            disabled={disabled}
            className="h-9 bg-background"
            required
          />
        </div>
      </div>
    )}
  </>
);
