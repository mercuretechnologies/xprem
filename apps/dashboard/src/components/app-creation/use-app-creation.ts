import { useEffect, useState } from 'react';
import {
  api,
  KeysMode,
  KeysConfig,
  CreateAppPayload,
  ExpoAccountApps,
  ExpoHistoryJobStatus,
  ExpoImportableApp,
  ExpoImportPlan,
  describeApiError,
} from '@/lib/api';
import { useToast } from '@/hooks/use-toast';

export type CreationMode = 'create' | 'import';
export type ImportStep = 'token' | 'pick' | 'keys' | 'preview' | 'history';

export const HISTORY_LIMIT_CHOICES = [10, 25, 50] as const;

type UseAppCreationParams = {
  onClose: () => void;
  onAppCreated?: (appId: string) => void;
};

// useAppCreation owns the whole modal state machine: the create form, the
// three-step Expo import and the polling of the background history job.
export const useAppCreation = ({ onClose, onAppCreated }: UseAppCreationParams) => {
  const { toast } = useToast();
  const [isSubmitting, setIsSubmitting] = useState(false);

  const [mode, setMode] = useState<CreationMode>('create');

  const [name, setName] = useState('');
  const [keysMode, setKeysMode] = useState<KeysMode>('database');
  const [publicSecretId, setPublicSecretId] = useState('');
  const [privateSecretId, setPrivateSecretId] = useState('');

  const [importStep, setImportStep] = useState<ImportStep>('token');
  const [accessToken, setAccessToken] = useState('');
  const [accounts, setAccounts] = useState<ExpoAccountApps[] | null>(null);
  const [selectedExpoApp, setSelectedExpoApp] = useState<ExpoImportableApp | null>(null);
  const [isLoadingApps, setIsLoadingApps] = useState(false);
  const [plan, setPlan] = useState<ExpoImportPlan | null>(null);
  const [isLoadingPlan, setIsLoadingPlan] = useState(false);

  const [includeHistory, setIncludeHistory] = useState(false);
  const [historyLimit, setHistoryLimit] = useState<number>(25);
  const [historyJobId, setHistoryJobId] = useState<string | null>(null);
  const [historyStatus, setHistoryStatus] = useState<ExpoHistoryJobStatus | null>(null);
  const [historyCancelRequested, setHistoryCancelRequested] = useState(false);

  // The job runs server-side; polling only mirrors it, so closing the modal
  // mid-import is safe.
  useEffect(() => {
    if (!historyJobId) {
      return;
    }
    let cancelled = false;
    let timer: number;
    const poll = async () => {
      try {
        const status = await api.getExpoImportJob(historyJobId);
        if (cancelled) {
          return;
        }
        setHistoryStatus(status);
        if (status.state === 'running') {
          timer = window.setTimeout(poll, 1500);
        }
      } catch {
        if (!cancelled) {
          timer = window.setTimeout(poll, 3000);
        }
      }
    };
    timer = window.setTimeout(poll, 500);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [historyJobId]);

  const resetForm = () => {
    setMode('create');
    setName('');
    setKeysMode('database');
    setPublicSecretId('');
    setPrivateSecretId('');
    setImportStep('token');
    setAccessToken('');
    setAccounts(null);
    setSelectedExpoApp(null);
    setPlan(null);
    setIncludeHistory(false);
    setHistoryLimit(25);
    setHistoryJobId(null);
    setHistoryStatus(null);
    setHistoryCancelRequested(false);
  };

  const handleClose = () => {
    resetForm();
    onClose();
  };

  const cancelHistoryImport = async () => {
    if (!historyJobId) {
      return;
    }
    setHistoryCancelRequested(true);
    try {
      await api.cancelExpoImportJob(historyJobId);
    } catch {
      setHistoryCancelRequested(false);
    }
  };

  const keysConfig: KeysConfig = {
    mode: keysMode,
    ...(keysMode === 'aws-secrets-manager' && {
      publicSecretId: publicSecretId.trim(),
      privateSecretId: privateSecretId.trim(),
    }),
  };

  const submitCreate = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim()) {
      toast({
        title: 'Validation Error',
        description: 'Please provide an application name.',
        variant: 'destructive',
      });
      return;
    }
    setIsSubmitting(true);
    const payload: CreateAppPayload = {
      name: name.trim(),
      keysConfig,
    };
    try {
      const response = await api.createApp(payload);
      toast({
        title: 'Success',
        description: `App "${name}" created successfully.`,
      });
      if (onAppCreated) {
        onAppCreated(response.appId);
      }
      handleClose();
    } catch (error) {
      const { title, description } = describeApiError(error, 'Error creating app');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsSubmitting(false);
    }
  };

  const loadExpoApps = async () => {
    if (!accessToken.trim()) {
      toast({
        title: 'Validation Error',
        description: 'Please paste an Expo access token.',
        variant: 'destructive',
      });
      return;
    }
    setIsLoadingApps(true);
    try {
      const response = await api.listExpoImportApps(accessToken.trim());
      setAccounts(response);
      setSelectedExpoApp(null);
      setImportStep('pick');
    } catch (error) {
      const { title, description } = describeApiError(error, 'Could not list your Expo apps');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsLoadingApps(false);
    }
  };

  // No import without its dry run: the keys step submits here, and the
  // preview step is what actually launches the import.
  const loadPlan = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!selectedExpoApp) {
      return;
    }
    setIsLoadingPlan(true);
    try {
      setPlan(await api.previewExpoImport(accessToken.trim(), selectedExpoApp.id));
      setImportStep('preview');
    } catch (error) {
      const { title, description } = describeApiError(error, 'Could not preview the import');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsLoadingPlan(false);
    }
  };

  const submitImport = async () => {
    if (!selectedExpoApp) {
      return;
    }
    setIsSubmitting(true);
    try {
      const result = await api.importExpoApp(accessToken.trim(), {
        expoAppId: selectedExpoApp.id,
        keysConfig,
        ...(includeHistory && { historyLimit }),
      });
      const skippedCount = result.skipped?.length ?? 0;
      toast({
        title: 'Import complete',
        description:
          `"${result.name}" imported with ${result.branchCount} branch${result.branchCount === 1 ? '' : 'es'} ` +
          `and ${result.channelCount} channel${result.channelCount === 1 ? '' : 's'}.` +
          (skippedCount > 0 ? ` ${skippedCount} entries were skipped.` : ''),
      });
      if (onAppCreated) {
        onAppCreated(result.appId);
      }
      if (result.historyJobId) {
        setHistoryJobId(result.historyJobId);
        setImportStep('history');
        return;
      }
      handleClose();
    } catch (error) {
      const { title, description } = describeApiError(error, 'Error importing app');
      toast({ title, description, variant: 'destructive' });
    } finally {
      setIsSubmitting(false);
    }
  };

  return {
    mode,
    setMode,
    isSubmitting,
    handleClose,

    name,
    setName,
    submitCreate,

    keysMode,
    setKeysMode,
    publicSecretId,
    setPublicSecretId,
    privateSecretId,
    setPrivateSecretId,

    importStep,
    setImportStep,
    accessToken,
    setAccessToken,
    accounts,
    selectedExpoApp,
    setSelectedExpoApp,
    isLoadingApps,
    loadExpoApps,
    plan,
    isLoadingPlan,
    loadPlan,
    submitImport,

    includeHistory,
    setIncludeHistory,
    historyLimit,
    setHistoryLimit,
    historyStatus,
    historyCancelRequested,
    cancelHistoryImport,
  };
};

export type AppCreation = ReturnType<typeof useAppCreation>;
