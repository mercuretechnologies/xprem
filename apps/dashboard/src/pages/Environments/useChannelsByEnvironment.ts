import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '@/lib/api';
import { useSelectedApp } from '@/lib/SelectedAppContext';

// Channel names grouped by the environment they point to, from the channels
// list: the environments endpoint does not carry the reverse mapping.
export const useChannelsByEnvironment = () => {
  const { selectedAppId } = useSelectedApp();
  const channelsQuery = useQuery({
    queryKey: ['channels', selectedAppId],
    queryFn: () => api.getChannels(),
    enabled: !!selectedAppId,
  });
  const channelsByEnvironment = useMemo(() => {
    const grouped = new Map<string, string[]>();
    for (const channel of channelsQuery.data ?? []) {
      if (!channel.environmentName) continue;
      grouped.set(channel.environmentName, [
        ...(grouped.get(channel.environmentName) ?? []),
        channel.releaseChannelName,
      ]);
    }
    return grouped;
  }, [channelsQuery.data]);
  return { channelsByEnvironment, channelsQuery };
};
