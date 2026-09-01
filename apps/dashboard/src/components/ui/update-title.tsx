import React from 'react';
import { splitPullRequestRefs } from '@/lib/update-format';
import type { RepoLinks } from '@/lib/repo-links';

type UpdateTitleProps = {
  title: string;
  links: RepoLinks | null;
};

// Links every `#123` in a title. The rows these sit in are clickable, so each
// link stops its click from reaching the row.
export const UpdateTitle: React.FC<UpdateTitleProps> = ({ title, links }) => {
  if (!links) return <>{title}</>;
  return (
    <>
      {splitPullRequestRefs(title).map((part, index) =>
        'pr' in part ? (
          <a
            key={index}
            href={links.pullUrl(part.pr)}
            target="_blank"
            rel="noreferrer"
            className="text-link hover:underline"
            onClick={event => event.stopPropagation()}>
            #{part.pr}
          </a>
        ) : (
          <React.Fragment key={index}>{part.text}</React.Fragment>
        )
      )}
    </>
  );
};
