import React from 'react';
import { Calendar } from 'lucide-react';
import { formatCompactTimestamp, formatTimestamp } from '@/lib/utils';

type TimestampCellProps = {
  dateString: string | null | undefined;
  showSeconds?: boolean;
  compact?: boolean;
};

export const TimestampCell: React.FC<TimestampCellProps> = ({
  dateString,
  showSeconds = false,
  compact = false,
}) => {
  const formattedDate = compact
    ? formatCompactTimestamp(dateString, showSeconds)
    : formatTimestamp(dateString, showSeconds);

  return (
    <span className="flex items-center gap-1.5 whitespace-nowrap text-muted-foreground">
      <Calendar className="h-3.5 w-3.5 text-muted-foreground/70" />
      {formattedDate ? (
        <span title={compact ? (formatTimestamp(dateString, true) ?? undefined) : undefined}>
          {formattedDate}
        </span>
      ) : (
        <span className="italic text-muted-foreground/60">Legacy record</span>
      )}
    </span>
  );
};
