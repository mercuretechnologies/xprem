import { useNavigate } from 'react-router';
import { Box } from 'lucide-react';
import { Badge } from '@/components/ui/badge';

export const ChannelBadges = ({ channelNames }: { channelNames: string[] }) => {
  const navigate = useNavigate();
  if (channelNames.length === 0) {
    return <span className="text-muted-foreground/60">None</span>;
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {channelNames.map(channelName => (
        <button
          key={channelName}
          type="button"
          onClick={event => {
            event.stopPropagation();
            navigate(`/channels/${encodeURIComponent(channelName)}`);
          }}>
          <Badge variant="outline" className="gap-1 font-normal hover:bg-accent">
            <Box className="h-3 w-3 text-muted-foreground" />
            {channelName}
          </Badge>
        </button>
      ))}
    </div>
  );
};
