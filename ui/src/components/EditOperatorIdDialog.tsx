import { useState } from "react";
import { Alert, TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import EditDialog from "@/components/EditDialog";
import { type Operator, updateOperator } from "@/queries/operator";
import { homeDomain } from "@/utils/operator";
import { RESTART_WARNING } from "@/utils/restart";

export default function EditOperatorIdDialog({
  operator,
  onClose,
}: {
  operator: Operator;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [mcc, setMcc] = useState(operator.mcc);
  const [mnc, setMnc] = useState(operator.mnc);

  const mutation = useMutation({
    mutationFn: updateOperator,
    onSuccess: (updated) => {
      queryClient.setQueryData(["operator"], updated);
      void queryClient.invalidateQueries({ queryKey: ["sip"] });
      void queryClient.invalidateQueries({ queryKey: ["diameter"] });
      onClose();
    },
  });

  const mccError = /^\d{3}$/.test(mcc) ? undefined : "3 digits";
  const mncError = /^\d{2,3}$/.test(mnc) ? undefined : "2 or 3 digits";
  const valid = mccError === undefined && mncError === undefined;
  const renamed =
    valid && homeDomain(mcc, mnc) !== homeDomain(operator.mcc, operator.mnc);

  return (
    <EditDialog
      title="Edit Operator ID"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ ...operator, mcc, mnc })}
      onClose={onClose}
    >
      <TextField
        label="MCC"
        value={mcc}
        onChange={(e) => setMcc(e.target.value.trim())}
        error={mccError !== undefined}
        helperText={mccError}
        placeholder="001"
        required
      />
      <TextField
        label="MNC"
        value={mnc}
        onChange={(e) => setMnc(e.target.value.trim())}
        error={mncError !== undefined}
        helperText={mncError}
        placeholder="01"
        required
      />
      {renamed && (
        <Alert severity="warning">
          The home network domain becomes{" "}
          <DomainName name={homeDomain(mcc, mnc)} />. {RESTART_WARNING}
        </Alert>
      )}
    </EditDialog>
  );
}
