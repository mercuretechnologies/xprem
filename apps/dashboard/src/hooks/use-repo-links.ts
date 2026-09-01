import { useMemo } from 'react';
import { useSelectedApp } from '@/lib/SelectedAppContext';
import { repoLinksFor, type RepoLinks } from '@/lib/repo-links';

// Null when the selected app has no repository, or it is not a GitHub one.
export const useRepoLinks = (): RepoLinks | null => {
  const { apps, selectedAppId } = useSelectedApp();
  return useMemo(
    () => repoLinksFor(apps.find(app => app.id === selectedAppId)?.repositoryUrl),
    [apps, selectedAppId]
  );
};
