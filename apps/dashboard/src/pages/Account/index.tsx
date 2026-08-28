import { useState } from 'react';
import { api, describeApiError } from '@/lib/api';
import { useSettings } from '@/lib/SettingsContext';
import { useCurrentUser } from '@/lib/CurrentUserContext';
import { useToast } from '@/hooks/use-toast';
import { Button } from '@/components/ui/button';
import { Badge } from '@/components/ui/badge';
import { Card, CardContent } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { PageHeader } from '@/components/PageHeader';
import { PasswordRulesChecklist } from '@/components/ui/password-rules-checklist';
import { isPasswordValid } from '@/lib/password-policy';
import { dashboardBasename } from '@/lib/basename.ts';

export const Account = () => {
  const { CONTROL_PLANE_ENABLED } = useSettings();
  const { user } = useCurrentUser();
  const { toast } = useToast();

  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);

  const passwordsMatch = newPassword === confirmPassword;
  const canSubmit =
    currentPassword.length > 0 && isPasswordValid(newPassword) && passwordsMatch && !isSubmitting;

  const handleChangePassword = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!canSubmit) return;
    setIsSubmitting(true);
    try {
      const stillSignedIn = await api.changeMyPassword({ currentPassword, newPassword });
      setCurrentPassword('');
      setNewPassword('');
      setConfirmPassword('');
      if (!stillSignedIn) {
        // The password changed, but no replacement session came back: every
        // session the account held is gone, this tab included. A full
        // navigation rather than a router transition, for the reason endSession
        // gives in lib/api.ts: the state held above the router never re-reads
        // the cleared tokens, and would keep issuing unauthenticated requests.
        window.location.assign(`${dashboardBasename()}/login`);
        return;
      }
      toast({
        title: 'Password updated',
        description: 'Your other sessions have been signed out.',
      });
    } catch (error) {
      toast({ ...describeApiError(error, 'Error updating password'), variant: 'destructive' });
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <div className="w-full">
      <PageHeader title="My account" description="The account you are currently signed in with." />

      <div className="space-y-4">
        <Card>
          <CardContent className="flex items-center justify-between p-5">
            <div>
              <p className="text-sm font-medium">{user?.email}</p>
              <p className="mt-0.5 text-xs text-muted-foreground">Signed-in account</p>
            </div>
            {user?.isAdmin ? <Badge>Admin</Badge> : <Badge variant="secondary">Member</Badge>}
          </CardContent>
        </Card>

        {CONTROL_PLANE_ENABLED ? (
          <Card>
            <CardContent className="p-5">
              <p className="text-sm font-medium">Change password</p>
              <form onSubmit={handleChangePassword} className="mt-4 max-w-sm space-y-4">
                <div className="space-y-1.5">
                  <Label htmlFor="current-password">Current password</Label>
                  <Input
                    id="current-password"
                    type="password"
                    autoComplete="current-password"
                    value={currentPassword}
                    onChange={e => setCurrentPassword(e.target.value)}
                    disabled={isSubmitting}
                  />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="new-password">New password</Label>
                  <Input
                    id="new-password"
                    type="password"
                    autoComplete="new-password"
                    value={newPassword}
                    onChange={e => setNewPassword(e.target.value)}
                    disabled={isSubmitting}
                  />
                  <PasswordRulesChecklist password={newPassword} />
                </div>
                <div className="space-y-1.5">
                  <Label htmlFor="confirm-password">Confirm new password</Label>
                  <Input
                    id="confirm-password"
                    type="password"
                    autoComplete="new-password"
                    value={confirmPassword}
                    onChange={e => setConfirmPassword(e.target.value)}
                    disabled={isSubmitting}
                  />
                  {confirmPassword.length > 0 && !passwordsMatch && (
                    <p className="text-xs text-destructive">Passwords do not match</p>
                  )}
                </div>
                <Button type="submit" disabled={!canSubmit}>
                  {isSubmitting ? 'Updating…' : 'Update password'}
                </Button>
              </form>
            </CardContent>
          </Card>
        ) : (
          <div className="rounded-xl border border-dashed bg-muted/30 p-8 text-center text-sm text-muted-foreground">
            On a stateless deployment this account is configured through the ADMIN_EMAIL and
            ADMIN_PASSWORD environment variables.
          </div>
        )}
      </div>
    </div>
  );
};
