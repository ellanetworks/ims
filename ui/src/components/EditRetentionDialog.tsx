import { useState } from "react";
import { Alert, TextField, Typography } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  type CallRecordRetention,
  updateCallRecordRetention,
} from "@/queries/callRecords";

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
  const [days, setDays] = useState(String(retention.days));

  const mutation = useMutation({
    mutationFn: updateCallRecordRetention,
    onSuccess: (updated) => {
      queryClient.setQueryData(["call-record-retention"], updated);
      onClose();
    },
  });

  const n = Number(days);
  const valid = /^\d+$/.test(days) && n >= MIN_DAYS && n <= MAX_DAYS;
  const reduced = valid && n < retention.days;

  return (
    <EditDialog
      title="Edit Call Record Retention Policy"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ days: n })}
      onClose={onClose}
    >
      <Typography variant="body2" color="textSecondary">
        Set the number of days to retain call records. After this period,
        records will be automatically deleted.
      </Typography>
      {reduced && (
        <Alert severity="warning">
          Reducing retention from {retention.days} to {n} days will permanently
          delete call records older than {n} day{n === 1 ? "" : "s"}.
        </Alert>
      )}
      <TextField
        label="Days"
        value={days}
        onChange={(e) => setDays(e.target.value.trim())}
        error={days !== "" && !valid}
        helperText={`${MIN_DAYS} to ${MAX_DAYS} days`}
        slotProps={{ htmlInput: { inputMode: "numeric" } }}
        required
      />
    </EditDialog>
  );
}
