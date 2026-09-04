import { ExternalLink, GitCommitHorizontal } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { gitRepositoryFor, type GitProvider } from '@/lib/git';

const BitbucketIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
    <path d="M.778 1.213a.768.768 0 0 0-.768.892l3.263 19.81c.084.5.515.868 1.022.873H19.95a.772.772 0 0 0 .77-.646l3.27-20.03a.768.768 0 0 0-.768-.891zM14.52 15.53H9.522L8.17 8.466h7.561z" />
  </svg>
);

const CursorIcon = ({ className }: { className?: string }) => (
  <svg viewBox="90 67 332 379" fill="currentColor" className={className} aria-hidden="true">
    <path d="m415.348 156.575-151.504-87.4699c-4.865-2.8094-10.868-2.8094-15.733 0l-151.4964 87.4699c-4.0897 2.361-6.6146 6.728-6.6146 11.458v176.383c0 4.73 2.5249 9.097 6.6146 11.459l151.5034 87.469c4.865 2.81 10.868 2.81 15.733 0l151.504-87.469c4.09-2.362 6.615-6.729 6.615-11.459v-176.383c0-4.73-2.525-9.097-6.615-11.458zm-9.517 18.528-146.254 253.319c-.989 1.707-3.599 1.01-3.599-.967v-165.871c0-3.314-1.771-6.38-4.645-8.044l-143.644-82.932c-1.707-.989-1.01-3.599.967-3.599h292.51c4.153 0 6.749 4.502 4.672 8.101h-.007z" />
  </svg>
);

const GitHubIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
    <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
  </svg>
);

const GitLabIcon = ({ className }: { className?: string }) => (
  <svg viewBox="0 0 24 24" fill="currentColor" className={className} aria-hidden="true">
    <path d="m23.6004 9.5927-.0337-.0862L20.3.9814a.851.851 0 0 0-.3362-.405.8748.8748 0 0 0-.9997.0539.8748.8748 0 0 0-.29.4399l-2.2055 6.748H7.5375l-2.2057-6.748a.8573.8573 0 0 0-.29-.4412.8748.8748 0 0 0-.9997-.0537.8585.8585 0 0 0-.3362.4049L.4332 9.5015l-.0325.0862a6.0657 6.0657 0 0 0 2.0119 7.0105l.0113.0087.03.0213 4.976 3.7264 2.462 1.8633 1.4995 1.1321a1.0085 1.0085 0 0 0 1.2197 0l1.4995-1.1321 2.4619-1.8633 5.006-3.7489.0125-.01a6.0682 6.0682 0 0 0 2.0094-7.003z" />
  </svg>
);

const providerStyles: Record<GitProvider, { icon: string; chip: string; chipIcon: string }> = {
  github: {
    icon: 'text-[#24292f] dark:text-white',
    chip: 'border-[#1F2328]/30 bg-card text-[#1F2328] dark:border-white/25 dark:text-white',
    chipIcon: 'bg-[#1F2328] text-white',
  },
  gitlab: {
    icon: 'text-[#FC6D26]',
    chip: 'border-[#FC6D26]/40 bg-card text-[#9B2C0E] dark:text-[#FC9A6C]',
    chipIcon: 'bg-[#FC6D26] text-white',
  },
  bitbucket: {
    icon: 'text-[#0052CC] dark:text-[#579DFF]',
    chip: 'border-[#0C66E4]/40 bg-card text-[#0052CC] dark:text-[#579DFF]',
    chipIcon: 'bg-[#0C66E4] text-white',
  },
  cursor: {
    icon: 'text-[#26251E] dark:text-[#F7F7F4]',
    chip: 'border-[#26251E]/30 bg-card text-[#26251E] dark:border-[#F7F7F4]/25 dark:text-[#F7F7F4]',
    chipIcon: 'bg-[#26251E] text-[#F7F7F4] dark:bg-[#F7F7F4] dark:text-[#26251E]',
  },
  generic: {
    icon: 'text-muted-foreground',
    chip: 'border-input bg-card text-foreground',
    chipIcon: 'bg-muted text-muted-foreground',
  },
};

const ProviderIcon = ({ provider, className }: { provider: GitProvider; className?: string }) => {
  if (provider === 'github') return <GitHubIcon className={className} />;
  if (provider === 'gitlab') return <GitLabIcon className={className} />;
  if (provider === 'bitbucket') return <BitbucketIcon className={className} />;
  if (provider === 'cursor') return <CursorIcon className={className} />;
  return <GitCommitHorizontal className={className} />;
};

export const GitCommitLink = ({
  commitHash,
  gitUrl,
  variant = 'chip',
}: {
  commitHash: string;
  gitUrl?: string;
  variant?: 'chip' | 'button';
}) => {
  if (!commitHash) return null;
  const repository = gitRepositoryFor(gitUrl);

  if (variant === 'button') {
    if (!repository) return null;
    return (
      <Button
        asChild
        size="sm"
        variant="outline"
        className="h-7 shrink-0 gap-1.5 px-2.5 text-xs text-muted-foreground hover:text-foreground">
        <a
          href={repository.commitUrl(commitHash)}
          target="_blank"
          rel="noreferrer"
          title={`Open commit ${commitHash} in ${repository.label}`}>
          <ProviderIcon
            provider={repository.provider}
            className={providerStyles[repository.provider].icon}
          />
          View on {repository.label}
        </a>
      </Button>
    );
  }

  const chip = (
    <span
      className={`inline-flex h-7 items-center overflow-hidden rounded-md border text-xs font-medium shadow-sm transition-all group-hover:-translate-y-px group-hover:shadow-md group-focus-visible:ring-2 group-focus-visible:ring-ring/40 ${
        providerStyles[repository?.provider ?? 'generic'].chip
      }`}
      title={commitHash || undefined}>
      <span
        className={`flex h-full w-7 items-center justify-center [&_svg]:h-3.5 [&_svg]:w-3.5 ${
          providerStyles[repository?.provider ?? 'generic'].chipIcon
        }`}>
        <ProviderIcon provider={repository?.provider ?? 'generic'} />
      </span>
      <code className="px-2 font-mono">
        {commitHash.length > 10 ? commitHash.slice(0, 8) : commitHash}
      </code>
      {repository && (
        <span className="flex h-full w-6 items-center justify-center border-l border-current/15 text-current/55 transition-colors group-hover:text-current [&_svg]:h-3 [&_svg]:w-3">
          <ExternalLink />
        </span>
      )}
    </span>
  );

  if (!repository) return chip;
  return (
    <a
      href={repository.commitUrl(commitHash)}
      target="_blank"
      rel="noreferrer"
      className="group inline-flex cursor-pointer rounded-md outline-none"
      aria-label={`Open commit ${commitHash} in ${repository.label}`}
      onClick={event => event.stopPropagation()}>
      {chip}
    </a>
  );
};
