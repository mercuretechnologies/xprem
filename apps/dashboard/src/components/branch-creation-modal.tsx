import React, { useState } from 'react';
import { api, ApiProblemError } from '@/lib/api';
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

type CreateBranchModalProps = {
  isOpen: boolean;
  onClose: () => void;
  // Called once the branch exists so the caller can use it straight away:
  // map it to a channel, prefill a form, ...
  onBranchCreated?: (branch: { branchId: string; branchName: string }) => void | Promise<void>;
};

export const CreateBranchModal = ({ isOpen, onClose, onBranchCreated }: CreateBranchModalProps) => {
  const { toast } = useToast();
  const [name, setName] = useState('');
  const [isSubmitting, setIsSubmitting] = useState(false);

  const handleClose = () => {
    setName('');
    onClose();
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    const branchName = name.trim();
    if (!branchName || isSubmitting) return;
    setIsSubmitting(true);
    try {
      const { branchId } = await api.createBranch(branchName);
      try {
        await onBranchCreated?.({ branchId, branchName });
        toast({
          title: 'Branch created',
          description: `"${branchName}" is ready to receive updates.`,
        });
      } catch (error) {
        const errorMessage =
          error instanceof Error
            ? error.message
            : 'Refresh the page to use the newly created branch.';
        toast({
          title: 'Branch created, but the page could not be updated',
          description: errorMessage,
          variant: 'destructive',
        });
      }
      handleClose();
    } catch (error) {
      let errorTitle = 'Error creating branch';
      let errorMessage = 'An unexpected error occurred.';
      if (error instanceof ApiProblemError) {
        errorTitle = error.title;
        errorMessage = error.detail;
      } else if (error instanceof Error) {
        errorMessage = error.message;
      }
      toast({ title: errorTitle, description: errorMessage, variant: 'destructive' });
    } finally {
      setIsSubmitting(false);
    }
  };

  return (
    <Dialog open={isOpen} onOpenChange={open => !open && handleClose()}>
      <DialogContent className="sm:max-w-[420px]">
        <form onSubmit={handleSubmit}>
          <DialogHeader>
            <DialogTitle className="text-lg">Create branch</DialogTitle>
            <DialogDescription>
              A branch is a line of updates you publish to. Point a release channel at it to start
              serving those updates to your apps.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-1.5 py-4">
            <Label htmlFor="new-branch-name" className="text-xs font-medium text-foreground">
              Branch name
            </Label>
            <Input
              id="new-branch-name"
              placeholder="e.g., production, staging, hotfix-1"
              value={name}
              onChange={e => setName(e.target.value)}
              disabled={isSubmitting}
              autoFocus
              className="h-9"
            />
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={handleClose} disabled={isSubmitting}>
              Cancel
            </Button>
            <Button type="submit" disabled={isSubmitting || !name.trim()}>
              {isSubmitting ? 'Creating...' : 'Create branch'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
};
