import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert.tsx';
import { AlertCircle } from 'lucide-react';
import { Button } from '@/components/ui/button';

export const ApiError = ({ error, onRetry }: { error: Error; onRetry?: () => void }) => {
  return (
    <Alert variant="destructive" className="w-max">
      <AlertCircle className="h-4 w-4" />
      <AlertTitle>An error occurred while fetching data</AlertTitle>
      <AlertDescription>{error.message}</AlertDescription>
      {onRetry && (
        <Button variant="outline" className="mt-3" onClick={onRetry}>
          Try again
        </Button>
      )}
    </Alert>
  );
};
