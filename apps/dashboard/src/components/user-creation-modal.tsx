import React, { useState } from 'react';
import { api, describeApiError } from '@/lib/api';
import { useToast } from '@/hooks/use-toast';
import { useQueryClient } from '@tanstack/react-query';
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
import { isPasswordValid } from '@/lib/password-policy';
import { usePermissions } from '@/ee/lib/PermissionsContext';
import { DraftGrant, GrantsEditor } from '@/ee/components/GrantsEditor';

type CreateUserModalProps = {
  isOpen: boolean;
  onClose: () => void;
  onUserCreated?: () => void;
};

export const CreateUserModal = ({ isOpen, onClose, onUserCreated }: CreateUserModalProps) => {
  const { toast } = useToast();
  const queryClient = useQueryClient();
  // While enterprise roles are enforced, a member created without a grant
  // sees an empty dashboard, so the modal offers the role assignment
  // directly. Without a license the modal keeps its community shape.
  const { rbacEnabled } = usePermissions();
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [isAdmin, setIsAdmin] = useState(false);
  const [grants, setGrants] = useState<DraftGrant[]>([]);
  const [isSubmitting, setIsSubmitting] = useState(false);

  const handleClose = () => {
    setEmail('');
    setPassword('');
    setIsAdmin(false);
    setGrants([]);
    onClose();
  };

  const canSubmit = email.trim().length > 0 && isPasswordValid(password);

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!canSubmit || isSubmitting) return;
    setIsSubmitting(true);
    try {
      const user = await api.createUser({ email: email.trim(), password, isAdmin });
      // The grants ride a second call; the account already exists if it
      // fails, so say exactly that instead of a generic creation error.
      if (!isAdmin && rbacEnabled && grants.length > 0) {
        try {
          await api.setUserGrants(user.id, grants);
          queryClient.invalidateQueries({ queryKey: ['userGrantsSummary'] });
        } catch (grantError) {
          toast({
            ...describeApiError(
              grantError,
              `"${user.email}" was created but assigning their roles failed. Open Roles on their row to retry.`
            ),
            variant: 'destructive',
          });
          onUserCreated?.();
          handleClose();
          return;
        }
      }
      toast({
        title: 'User created',
        description: `"${user.email}" can now sign in to the dashboard.`,
      });
      queryClient.invalidateQueries({ queryKey: ['userGrantsSummary'] });
      onUserCreated?.();
      handleClose();
    } catch (error) {
      toast({ ...describeApiError(error, 'Error creating user'), variant: 'destructive' });
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={isOpen} onOpenChange={open => !open && handleClose()}>
      <DialogContent
        className={
          rbacEnabled ? 'max-h-[85vh] overflow-y-auto sm:max-w-[560px]' : 'sm:max-w-[420px]'
        }>
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle className="text-lg">Create user</DialogTitle>
            <DialogDescription>
              The user signs in with this email and password. Admins can additionally manage users,
              create apps and remap release channels.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4 py-4">
            <div className="space-y-1.5">
              <Label htmlFor="new-user-email" className="text-xs font-medium text-foreground">
                Email
              </Label>
              <Input
                id="new-user-email"
                type="email"
                placeholder="e.g., jane@acme.dev"
                value={email}
                onChange={e => setEmail(e.target.value)}
                disabled={isSubmitting}
                autoFocus
                className="h-9"
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="new-user-password" className="text-xs font-medium text-foreground">
                Password
              </Label>
              <Input
                id="new-user-password"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={e => setPassword(e.target.value)}
                disabled={isSubmitting}
                className="h-9"
              />
              <PasswordRulesChecklist password={password} />
            </div>
            <label className="flex items-center gap-2 text-sm" htmlFor="new-user-admin">
              <input
                id="new-user-admin"
                type="checkbox"
                checked={isAdmin}
                onChange={e => setIsAdmin(e.target.checked)}
                disabled={isSubmitting}
                className="h-4 w-4 rounded border-input accent-primary"
              />
              <span>Admin account</span>
            </label>
            {rbacEnabled && !isAdmin && (
              <div className="space-y-2 border-t pt-4">
                <p className="text-xs font-medium text-foreground">App access</p>
                <p className="text-xs text-muted-foreground">
                  Members only see the apps you grant them. Without any grant this account will see
                  an empty dashboard.
                </p>
                <GrantsEditor draft={grants} onChange={setGrants} disabled={isSubmitting} />
              </div>
            )}
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={handleClose} disabled={isSubmitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={isSubmitting || !canSubmit}>
              {isSubmitting ? 'Creating...' : 'Create user'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
};
