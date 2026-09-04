import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { CreateStep } from './create-step';
import { TokenStep } from './token-step';
import { PickStep } from './pick-step';
import { KeysStep } from './keys-step';
import { PreviewStep } from './preview-step';
import { HistoryStep } from './history-step';
import { CreationMode, useAppCreation } from './use-app-creation';

type CreateAppModalProps = {
  isOpen: boolean;
  onClose: () => void;
  onAppCreated?: (appId: string) => void;
};

export const CreateAppModal = ({ isOpen, onClose, onAppCreated }: CreateAppModalProps) => {
  const creation = useAppCreation({ onClose, onAppCreated });
  const { mode, importStep } = creation;

  const description =
    mode === 'create'
      ? 'Each application gets its own branches, channels, tokens and signing keys.'
      : importStep === 'history'
        ? 'The app is imported. Its newest updates are now being copied into your storage.'
        : importStep === 'preview'
          ? 'Nothing is created yet. Review what the import will do, then confirm.'
          : 'The app keeps its Expo project ID, and its branches, channels and mappings are copied over. Your token is only used for the import and never stored.';

  return (
    <Dialog open={isOpen} onOpenChange={open => !open && creation.handleClose()}>
      <DialogContent className="sm:max-w-[480px]">
        <DialogHeader>
          <DialogTitle className="text-lg">
            {mode === 'create' ? 'New application' : 'Import from Expo'}
          </DialogTitle>
          <DialogDescription className="text-sm">{description}</DialogDescription>
        </DialogHeader>

        {importStep !== 'history' && importStep !== 'preview' && (
          <div className="grid grid-cols-2 gap-1 rounded-lg border border-border bg-muted/30 p-1">
            {(
              [
                { id: 'create', label: 'Create new' },
                { id: 'import', label: 'Import from Expo' },
              ] as { id: CreationMode; label: string }[]
            ).map(tab => (
              <button
                key={tab.id}
                type="button"
                disabled={creation.isSubmitting || creation.isLoadingApps}
                onClick={() => creation.setMode(tab.id)}
                className={`h-8 rounded-md text-xs font-medium transition-colors ${
                  mode === tab.id
                    ? 'bg-background text-foreground shadow-sm border border-border'
                    : 'text-muted-foreground hover:text-foreground'
                }`}>
                {tab.label}
              </button>
            ))}
          </div>
        )}

        {mode === 'create' && <CreateStep creation={creation} />}
        {mode === 'import' && importStep === 'token' && <TokenStep creation={creation} />}
        {mode === 'import' && importStep === 'pick' && <PickStep creation={creation} />}
        {mode === 'import' && importStep === 'keys' && <KeysStep creation={creation} />}
        {mode === 'import' && importStep === 'preview' && <PreviewStep creation={creation} />}
        {mode === 'import' && importStep === 'history' && <HistoryStep creation={creation} />}
      </DialogContent>
    </Dialog>
  );
};
