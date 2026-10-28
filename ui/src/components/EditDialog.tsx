import type { FormEvent, ReactNode } from "react";
import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
} from "@mui/material";
import DialogError from "@/components/DialogError";

interface EditDialogProps {
  title: string;
  valid: boolean;
  pending: boolean;
  error: Error | null;
  onSubmit: () => void;
  onClose: () => void;
  children: ReactNode;
  // The labels of the submit button and of its error, for a dialog that does not update.
  submitLabel?: string;
  pendingLabel?: string;
  failure?: string;
}

export default function EditDialog({
  title,
  valid,
  pending,
  error,
  onSubmit,
  onClose,
  children,
  submitLabel = "Update",
  pendingLabel = "Updating…",
  failure = "Could not update",
}: EditDialogProps) {
  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (!valid || pending) return;
    onSubmit();
  };

  return (
    <Dialog
      open
      onClose={onClose}
      fullWidth
      maxWidth="sm"
      slotProps={{ paper: { component: "form", onSubmit: submit } }}
    >
      <DialogTitle>{title}</DialogTitle>
      <DialogContent dividers>
        <Stack spacing={2}>
          <DialogError error={error} prefix={failure} />
          {children}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          type="submit"
          variant="contained"
          color="success"
          disabled={!valid || pending}
        >
          {pending ? pendingLabel : submitLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
