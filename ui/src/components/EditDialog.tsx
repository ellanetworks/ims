import { useEffect, useRef, type FormEvent, type ReactNode } from "react";
import {
  Box,
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
  submitLabel?: string;
  pendingLabel?: string;
  failure?: string;
  notice?: ReactNode;
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
  notice,
}: EditDialogProps) {
  const content = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (error && content.current) content.current.scrollTop = 0;
  }, [error]);

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
      <DialogContent dividers ref={content}>
        <Stack spacing={2}>
          <DialogError error={error} prefix={failure} />
          {children}
        </Stack>
      </DialogContent>
      {notice && <Box sx={{ px: 3, pt: 2 }}>{notice}</Box>}
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
