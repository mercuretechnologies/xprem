import { useState } from 'react';
import { api, describeApiError, UserRecord } from '@/lib/api';
import { useToast } from '@/hooks/use-toast';
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { PasswordRulesChecklist } from '@/components/ui/password-rules-checklist';
import { isPasswordValid, PASSWORD_MIN_LENGTH } from '@/lib/password-policy';

type ResetUserPasswordDialogProps = {
  user: UserRecord | null;
  onClose: () => void;
};

export const ResetUserPasswordDialog = ({ user, onClose }: ResetUserPasswordDialogProps) => {
  const { toast } = useToast();
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);

  const passwordsMatch = newPassword === confirmPassword;
  const canSubmit = isPasswordValid(newPassword) && passwordsMatch && !isSubmitting;

  const handleClose = () => {
    if (isSubmitting) return;
    setNewPassword('');
    setConfirmPassword('');
    onClose();
  };

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!user || !canSubmit) return;
    setIsSubmitting(true);
    try {
      await api.resetUserPassword(user.id, newPassword);
      toast({
        title: 'Password reset',
        description: `The password for "${user.email}" was updated. Their existing sessions have been signed out.`,
      });
      setNewPassword('');
      setConfirmPassword('');
      onClose();
    } catch (error) {
      toast({ ...describeApiError(error, 'Error resetting password'), variant: 'destructive' });
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={user !== null} onOpenChange={open => !open && handleClose()}>
      <DialogContent className="sm:max-w-[420px]">
        <form id="reset-user-password" method="post" onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle>Reset password</DialogTitle>
            <DialogDescription>
              Set a new password for {user?.email}. This signs out their existing sessions. Share
              the new password with them securely.
            </DialogDescription>
          </DialogHeader>
          {/* Associate the generated password with the target account, rather
              than the administrator's saved login on the same dashboard. */}
          <Input
            id="reset-user-username"
            name="username"
            type="text"
            autoComplete="username"
            value={user?.email ?? ''}
            readOnly
            className="hidden"
            aria-hidden="true"
          />
          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="reset-user-new-password">New password</Label>
              <Input
                id="reset-user-new-password"
                name="new-password"
                type="password"
                autoComplete="new-password"
                minLength={PASSWORD_MIN_LENGTH}
                required
                value={newPassword}
                onChange={event => setNewPassword(event.target.value)}
                readOnly={isSubmitting}
                autoFocus
              />
              <PasswordRulesChecklist password={newPassword} />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="reset-user-confirm-password">Confirm new password</Label>
              <Input
                id="reset-user-confirm-password"
                name="confirm-password"
                type="password"
                autoComplete="new-password"
                minLength={PASSWORD_MIN_LENGTH}
                required
                value={confirmPassword}
                onChange={event => setConfirmPassword(event.target.value)}
                readOnly={isSubmitting}
              />
              {confirmPassword.length > 0 && !passwordsMatch && (
                <p className="text-xs text-destructive">Passwords do not match</p>
              )}
            </div>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={handleClose} disabled={isSubmitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {isSubmitting ? 'Resetting…' : 'Reset password'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
};
