import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  type CallRecordRetention,
  updateCallRecordRetention,
} from "@/queries/callRecords";

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
  const valid = /^\d+$/.test(days) && n >= 1 && n <= MAX_DAYS;

  return (
    <EditDialog
      title="Edit Call Record Retention"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ days: n })}
      onClose={onClose}
    >
      <TextField
        label="Days"
        value={days}
        onChange={(e) => setDays(e.target.value.trim())}
        error={days !== "" && !valid}
        helperText={`Records older than this are deleted within the hour. 1 to ${MAX_DAYS}.`}
        slotProps={{ htmlInput: { inputMode: "numeric" } }}
        required
      />
    </EditDialog>
  );
}
