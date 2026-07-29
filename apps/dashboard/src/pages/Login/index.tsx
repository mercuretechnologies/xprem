import { TriangleAlert } from 'lucide-react';
import { XpremMark } from '@/components/xprem-mark';
import { Input } from '@/components/ui/input.tsx';
import { Button } from '@/components/ui/button.tsx';
import { Label } from '@/components/ui/label.tsx';
import { useForm } from 'react-hook-form';
import { z } from 'zod';
import { zodResolver } from '@hookform/resolvers/zod';
import { Form, FormControl, FormField, FormItem, FormMessage } from '@/components/ui/form.tsx';
import { useCallback, useEffect, useState } from 'react';
import { isAuthenticated, setTokens } from '@/lib/auth.ts';
import { Navigate, useNavigate } from 'react-router';
import { api, ApiProblemError } from '@/lib/api.ts';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { SsoLoginButton } from '@/ee/components/SsoLoginButton';

const FormSchema = z.object({
  email: z.string().email({
    message: 'Enter a valid email address',
  }),
  password: z.string().min(1, {
    message: 'Enter your password',
  }),
});

// Messages behind the ssoError codes of the SSO callback redirect. The
// underlying cause stays in the server logs; the code alone reaches the URL.
const SSO_ERROR_MESSAGES: Record<string, string> = {
  sso_denied: 'Sign-in was cancelled at the identity provider.',
  sso_license:
    'Single sign-on requires an active enterprise license. Sign in with your password instead.',
  sso_email_missing:
    'Your identity provider did not return an email address for your account. Contact your administrator.',
  sso_email_unverified:
    'Your identity provider did not verify your email address. Contact your administrator.',
  sso_forbidden:
    'Your account is not allowed to access this dashboard. Contact your administrator.',
  sso_pending:
    'Your account has been created and is waiting for an administrator to approve it. You will be able to sign in once it is approved.',
  sso_failed:
    'SSO sign-in failed. Try again, and contact your administrator if it keeps happening.',
  sso_throttled: 'Too many sign-in attempts from your network. Wait a few minutes and try again.',
};

export const Login = () => {
  const form = useForm<z.infer<typeof FormSchema>>({
    resolver: zodResolver(FormSchema),
    defaultValues: {
      email: '',
      password: '',
    },
  });
  const navigate = useNavigate();
  // Shared by both sign-in paths: the SSO callback's error codes and the
  // password path's 403s (SSO enforced, account awaiting approval). Both are
  // account-state messages rather than credential mistakes.
  const [signInNotice, setSignInNotice] = useState<string | null>(null);

  // The SSO callback lands here with the session pair (or an error code) in
  // the URL fragment: fragments never reach a server, so tokens cannot end up
  // in any access log. The fragment is stripped before anything else so it
  // does not survive in the address bar or the session history either.
  useEffect(() => {
    const hash = window.location.hash;
    if (hash.length <= 1) {
      return;
    }
    const params = new URLSearchParams(hash.slice(1));
    const ssoToken = params.get('ssoToken');
    const ssoRefreshToken = params.get('ssoRefreshToken');
    const errorCode = params.get('ssoError');
    // Any token-looking material or error code strips the fragment, even a
    // lone half of the pair: nothing sensitive may survive in the URL.
    if (!ssoToken && !ssoRefreshToken && !errorCode) {
      return;
    }
    window.history.replaceState(null, '', window.location.pathname + window.location.search);
    if (ssoToken && ssoRefreshToken) {
      setTokens(ssoToken, ssoRefreshToken);
      navigate('/');
      return;
    }
    setSignInNotice(SSO_ERROR_MESSAGES[errorCode ?? ''] ?? SSO_ERROR_MESSAGES.sso_failed);
  }, [navigate]);

  const onSubmit = useCallback(
    async (data: z.infer<typeof FormSchema>) => {
      try {
        const response = await api.login(data.email, data.password);
        setTokens(response.token, response.refreshToken);
        navigate('/');
      } catch (error) {
        // A 403 means the credentials were right but the account may not use
        // this door: SSO is enforced for it, or it is waiting for approval.
        // That is not a password mistake, so it belongs in the notice above
        // the form rather than under the password field.
        if (error instanceof ApiProblemError && error.status === 403) {
          setSignInNotice(error.detail);
          return;
        }
        // Only an actual 401 means wrong credentials. A misconfigured server
        // (e.g. ADMIN_EMAIL not set in stateless mode) answers with an
        // actionable detail, and a fetch that never reached the server must
        // not masquerade as a credentials problem.
        let message = 'Could not reach the server. Check that it is running.';
        if (error instanceof ApiProblemError) {
          message = error.status === 401 ? 'Invalid email or password' : error.detail;
        }
        form.setError('password', {
          type: 'server',
          message,
        });
      }
    },
    [form, navigate]
  );

  // Declarative redirect rather than an effect: after the SSO callback the
  // tokens land during the mount effects, where App's own "not logged in"
  // bounce (closured on a stale value) would win the navigation race. It also
  // sends an already-signed-in visitor of /login straight to the dashboard.
  if (isAuthenticated()) {
    return <Navigate to="/" replace />;
  }

  return (
    <div className="flex min-h-screen w-full items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm">
        <div className="rounded-lg border bg-card p-8 shadow-elevated">
          <div className="mb-8 flex flex-col items-center gap-3 text-center">
            <XpremMark className="h-11 w-11 rounded-lg" />
            <div className="space-y-1">
              <h1 className="font-display text-lg font-semibold tracking-tight text-foreground">
                xprem
                <span
                  aria-hidden
                  className="ml-1 inline-block h-1.5 w-1.5 rounded-full bg-primary"
                />
              </h1>
              <p className="text-sm text-muted-foreground">
                Sign in to manage your over-the-air updates
              </p>
            </div>
          </div>

          {signInNotice && (
            <Alert variant="destructive" className="mb-5">
              <TriangleAlert className="h-4 w-4" />
              <AlertDescription>{signInNotice}</AlertDescription>
            </Alert>
          )}

          <Form {...form}>
            <form onSubmit={form.handleSubmit(onSubmit)} className="flex flex-col gap-5">
              <FormField
                control={form.control}
                name="email"
                render={({ field, fieldState }) => {
                  return (
                    <FormItem className="space-y-1.5">
                      <Label htmlFor="login-email">Email</Label>
                      <FormControl>
                        <Input
                          id="login-email"
                          type="email"
                          autoFocus
                          autoComplete="username"
                          {...field}
                        />
                      </FormControl>
                      <FormMessage>{fieldState.error?.message}</FormMessage>
                    </FormItem>
                  );
                }}
              />
              <FormField
                control={form.control}
                name="password"
                render={({ field, fieldState }) => {
                  return (
                    <FormItem className="space-y-1.5">
                      <Label htmlFor="login-password">Password</Label>
                      <FormControl>
                        <Input
                          id="login-password"
                          type="password"
                          autoComplete="current-password"
                          {...field}
                        />
                      </FormControl>
                      <FormMessage>{fieldState.error?.message}</FormMessage>
                    </FormItem>
                  );
                }}
              />
              <Button type="submit" className="w-full" disabled={form.formState.isSubmitting}>
                {form.formState.isSubmitting ? 'Signing in…' : 'Sign in'}
              </Button>
            </form>
          </Form>

          <SsoLoginButton />
        </div>

        <p className="mt-6 text-center text-xs text-muted-foreground">
          Self-hosted OTA updates for Expo apps
        </p>
      </div>
    </div>
  );
};
