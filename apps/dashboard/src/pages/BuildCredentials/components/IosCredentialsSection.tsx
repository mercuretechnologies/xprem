import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError } from '@/components/APIError';
import { api, AppIdentifier } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { AdHocIphones } from './AdHocIphones';
import { AppleAccountCard } from './AppleAccountCard';
import { IosSigningSection } from './IosSigningSection';

type Props = {
  identifier: AppIdentifier;
  canManage: boolean;
};

/** Loads the identifier signing state and the app Apple account metadata. */
export const IosCredentialsSection = ({ identifier, canManage }: Props) => {
  const { selectedAppId } = useSelectedApp();
  const queryClient = useQueryClient();

  const credentialsQuery = useQuery({
    queryKey: ['iosCredentials', selectedAppId, identifier.id],
    queryFn: () => api.getIosCredentials(identifier.id),
    enabled: !!selectedAppId,
  });

  const apiKeyQuery = useQuery({
    queryKey: ['appleApiKey', selectedAppId],
    queryFn: () => api.getAppleApiKey(),
    enabled: !!selectedAppId,
  });

  /** Refreshes the identifier signing state after a credential change. */
  const invalidate = () => {
    queryClient.invalidateQueries({ queryKey: ['iosCredentials', selectedAppId, identifier.id] });
  };

  // The key is per app: the configured badge of every identifier and the Apple lists depend on it.
  /** Refreshes Apple-dependent queries after the team API key changes. */
  const invalidateApiKey = () => {
    queryClient.invalidateQueries({ queryKey: ['appleApiKey', selectedAppId] });
    queryClient.invalidateQueries({ queryKey: ['identifiers', selectedAppId] });
    queryClient.invalidateQueries({ queryKey: ['appleDistributionCertificates', selectedAppId] });
    queryClient.invalidateQueries({ queryKey: ['iosDevices', selectedAppId] });
  };

  if (credentialsQuery.isPending || apiKeyQuery.isPending) {
    return <Skeleton className="h-48 w-full rounded-xl" />;
  }

  if (credentialsQuery.isError) {
    return (
      <ApiError error={credentialsQuery.error} onRetry={() => void credentialsQuery.refetch()} />
    );
  }

  const metadata = credentialsQuery.data;
  const apiKey = apiKeyQuery.data ?? null;

  return (
    <div className="space-y-4">
      <AppleAccountCard
        apiKey={apiKey}
        apiKeyError={apiKeyQuery.error}
        onRetry={() => void apiKeyQuery.refetch()}
        canManage={canManage}
        onChanged={invalidateApiKey}
      />
      <IosSigningSection
        identifierId={identifier.id}
        setting={metadata.signing}
        hasApiKey={!!apiKey}
        canManage={canManage}
        onChanged={invalidate}
      />
      <Card>
        <CardHeader className="space-y-1 border-b py-4">
          <CardTitle className="text-base">Apple Devices</CardTitle>
          <p className="text-sm text-muted-foreground">
            Install builds directly on registered iPhones, without TestFlight.
          </p>
        </CardHeader>
        <CardContent className="pt-4">
          {apiKey ? (
            <AdHocIphones canManage={canManage} />
          ) : (
            <p className="text-sm text-muted-foreground">Connect App Store Connect first.</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
};
