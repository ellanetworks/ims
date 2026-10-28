import { useState } from "react";
import { Alert, TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  type CallRecordRetention,
  updateCallRecordRetention,
} from "@/queries/callRecords";
import { useSnackbar } from "@/contexts/SnackbarContext";

const MIN_DAYS = 1;
const MAX_DAYS = 3650;

export default function EditRetentionDialog({
  retention,
  onClose,
}: {
  retention: CallRecordRetention;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [days, setDays] = useState(String(retention.days));

  const mutation = useMutation({
    mutationFn: updateCallRecordRetention,
    onSuccess: (updated) => {
      queryClient.setQueryData(["call-record-retention"], updated);
      showSnackbar("Retention updated successfully.", "success");
      onClose();
    },
  });

  const n = Number(days);
  const valid = /^\d+$/.test(days) && n >= MIN_DAYS && n <= MAX_DAYS;
  const reduced = valid && n < retention.days;

  return (
    <EditDialog
      title="Edit Retention"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ days: n })}
      onClose={onClose}
    >
      {reduced && (
        <Alert severity="warning">
          Reducing retention from {retention.days} to {n} days will permanently
          delete call records older than {n} day{n === 1 ? "" : "s"}.
        </Alert>
      )}
      <TextField
        label="Days"
        type="number"
        value={days}
        onChange={(e) => setDays(e.target.value)}
        error={!valid}
        helperText={
          valid
            ? "Call records older than this are deleted."
            : `A whole number of days, ${MIN_DAYS} to ${MAX_DAYS}.`
        }
        slotProps={{ htmlInput: { min: MIN_DAYS, max: MAX_DAYS, step: 1 } }}
        required
      />
    </EditDialog>
  );
}
