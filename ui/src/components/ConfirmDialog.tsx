import type { ReactNode } from "react";
import {
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  type ButtonProps,
} from "@mui/material";

export default function ConfirmDialog({
  title,
  description,
  confirmLabel,
  pendingLabel,
  pending,
  onConfirm,
  onClose,
  confirmColor = "error",
  confirmDisabled = false,
  maxWidth = "xs",
  children,
}: {
  title: string;
  description: ReactNode;
  confirmLabel: string;
  pendingLabel?: string;
  pending: boolean;
  onConfirm: () => void;
  onClose: () => void;
  confirmColor?: ButtonProps["color"];
  confirmDisabled?: boolean;
  maxWidth?: "xs" | "sm";
  children?: ReactNode;
}) {
  return (
    <Dialog open onClose={onClose} fullWidth maxWidth={maxWidth}>
      <DialogTitle>{title}</DialogTitle>
      <DialogContent>
        <DialogContentText>{description}</DialogContentText>
        {children}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button
          variant="contained"
          color={confirmColor}
          onClick={onConfirm}
          disabled={pending || confirmDisabled}
        >
          {pending && pendingLabel ? pendingLabel : confirmLabel}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
