import { useEffect, useMemo, useState } from 'react';
import { Navigate, useLocation, useSearchParams } from 'react-router';
import { KeyRound, TriangleAlert } from 'lucide-react';
import { Button } from '@/components/ui/button.tsx';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { api, describeApiError } from '@/lib/api.ts';
import { clearReturnTo, isAuthenticated, saveReturnTo } from '@/lib/auth.ts';

const REQUIRED_PARAMS = [
  'client_id',
  'redirect_uri',
  'response_type',
  'code_challenge',
  'code_challenge_method',
];

// The OAuth consent screen: an external MCP client asked for access, the
// authorize endpoint validated the request and bounced it here with the
// registered client name resolved server-side. Approving posts the request
// back with the session and follows the redirect that delivers the code.
export const OAuthConsent = () => {
  const location = useLocation();
  const [searchParams] = useSearchParams();
  const [submitting, setSubmitting] = useState<'approve' | 'deny' | null>(null);
  const [error, setError] = useState<string | null>(null);

  const loggedIn = isAuthenticated();
  useEffect(() => {
    if (loggedIn) {
      clearReturnTo();
    }
  }, [loggedIn]);

  const clientName = searchParams.get('client_name') || 'An application';
  const redirectHost = useMemo(() => {
    try {
      return new URL(searchParams.get('redirect_uri') ?? '').host || null;
    } catch {
      return null;
    }
  }, [searchParams]);

  if (!loggedIn) {
    // Idempotent write, safe during render; Login sends the user back here.
    saveReturnTo(location.pathname + location.search);
    return <Navigate to="/login" replace />;
  }

  const invalidRequest = REQUIRED_PARAMS.some(name => !searchParams.get(name));

  const decide = async (decision: 'approve' | 'deny') => {
    setSubmitting(decision);
    setError(null);
    try {
      const { redirectUrl } = await api.submitOAuthConsent(searchParams, decision);
      window.location.assign(redirectUrl);
    } catch (submitError) {
      setSubmitting(null);
      const { title, description } = describeApiError(
        submitError,
        'Could not record your decision'
      );
      setError(description || title);
    }
  };

  return (
    <div className="flex min-h-screen w-full items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="rounded-lg border bg-card p-8 shadow-elevated">
          <div className="mb-6 flex flex-col items-center gap-3 text-center">
            <div className="flex h-11 w-11 items-center justify-center rounded-lg border border-primary/30 bg-primary/10 text-primary">
              <KeyRound className="h-5 w-5" strokeWidth={2} />
            </div>
            <div className="space-y-1">
              <h1 className="font-display text-lg font-semibold tracking-tight text-foreground">
                Connection request
              </h1>
              <p className="text-sm text-muted-foreground">
                <span className="font-medium text-foreground">{clientName}</span> wants to access
                your xprem account.
              </p>
            </div>
          </div>

          {invalidRequest ? (
            <Alert variant="destructive">
              <TriangleAlert className="h-4 w-4" />
              <AlertDescription>
                This authorization request is invalid or incomplete. Close this tab and start again
                from the application that opened it.
              </AlertDescription>
            </Alert>
          ) : (
            <>
              <div className="mb-6 rounded-md border bg-background/50 p-4 text-sm text-muted-foreground">
                <p>
                  It will act through the MCP server with your account&apos;s own permissions: the
                  apps and updates you can see and manage, it can too.
                </p>
                {redirectHost && (
                  <p className="mt-2">
                    You will be sent back to{' '}
                    <span className="font-mono text-xs text-foreground">{redirectHost}</span>.
                  </p>
                )}
              </div>

              {error && (
                <Alert variant="destructive" className="mb-5">
                  <TriangleAlert className="h-4 w-4" />
                  <AlertDescription>{error}</AlertDescription>
                </Alert>
              )}

              <div className="flex gap-3">
                <Button
                  variant="outline"
                  className="flex-1"
                  disabled={submitting !== null}
                  onClick={() => decide('deny')}>
                  {submitting === 'deny' ? 'Denying…' : 'Deny'}
                </Button>
                <Button
                  className="flex-1"
                  disabled={submitting !== null}
                  onClick={() => decide('approve')}>
                  {submitting === 'approve' ? 'Authorizing…' : 'Authorize'}
                </Button>
              </div>
            </>
          )}
        </div>

        <p className="mt-6 text-center text-xs text-muted-foreground">
          Only authorize applications you trust
        </p>
      </div>
    </div>
  );
};
