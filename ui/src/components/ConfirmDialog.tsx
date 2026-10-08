import type { ReactNode } from "react";
import {
  Alert,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
} from "@mui/material";

interface ConfirmDialogProps {
  title: ReactNode;
  action: string;
  pending: boolean;
  error: Error | null;
  onConfirm: () => void;
  onClose: () => void;
  children?: ReactNode;
}

export default function ConfirmDialog({
  title,
  action,
  pending,
  error,
  onConfirm,
  onClose,
  children,
}: ConfirmDialogProps) {
  return (
    <Dialog open onClose={onClose} fullWidth maxWidth="sm">
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <Stack spacing={2}>
          {error && (
            <Alert severity="error">
              Could not {action.toLowerCase()}: {error.message}
            </Alert>
          )}
          {children}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          color="error"
          onClick={onConfirm}
          disabled={pending}
        >
          {action}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
