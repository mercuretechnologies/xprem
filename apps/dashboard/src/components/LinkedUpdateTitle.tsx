import { Fragment } from 'react';
import { gitRepositoryFor } from '@/lib/git';
import { splitReviewReferences } from '@/lib/update-format';

export const LinkedUpdateTitle = ({ title, gitUrl }: { title: string; gitUrl?: string }) => {
  const review = gitRepositoryFor(gitUrl)?.review;
  if (!review) return <>{title}</>;

  return (
    <>
      {splitReviewReferences(title, review.marker).map((part, index) =>
        'number' in part ? (
          <a
            key={index}
            href={review.url(part.number)}
            target="_blank"
            rel="noreferrer"
            className="text-link hover:underline"
            onClick={event => event.stopPropagation()}>
            {review.marker}
            {part.number}
          </a>
        ) : (
          <Fragment key={index}>{part.text}</Fragment>
        )
      )}
    </>
  );
};
