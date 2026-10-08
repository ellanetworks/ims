import type { FormEvent, ReactNode } from "react";
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
} from "@mui/material";

interface EditDialogProps {
  title: string;
  submitLabel?: string;
  pendingLabel?: string;
  valid: boolean;
  pending: boolean;
  error: Error | null;
  onSubmit: () => void;
  onClose: () => void;
  children: ReactNode;
}

export default function EditDialog({
  title,
  submitLabel = "Update",
  pendingLabel = "Updating…",
  valid,
  pending,
  error,
  onSubmit,
  onClose,
  children,
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
      <DialogContent>
        <Stack spacing={2} sx={{ pt: 1 }}>
          {error && (
            <Alert severity="error">
              Could not {submitLabel.toLowerCase()}: {error.message}
            </Alert>
          )}
          {children}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button type="submit" variant="contained" disabled={!valid || pending}>
          {pending ? pendingLabel : submitLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
