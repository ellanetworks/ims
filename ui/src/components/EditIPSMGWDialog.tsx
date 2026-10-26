import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { type IPSMGW, updateIPSMGW } from "@/queries/ipsmgw";

const E164 = /^\d{1,15}$/;

export default function EditIPSMGWDialog({
  ipsmgw,
  onClose,
}: {
  ipsmgw: IPSMGW;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [number, setNumber] = useState(ipsmgw.number);

  const mutation = useMutation({
    mutationFn: updateIPSMGW,
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["ip-sm-gw"] });
      onClose();
    },
  });

  const next = number.trim();
  const error =
    next === "" || E164.test(next)
      ? undefined
      : "An E.164 number of up to 15 digits, without +";

  return (
    <EditDialog
      title="Edit IP-SM-GW Number"
      valid={error === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate(next)}
      onClose={onClose}
    >
      <TextField
        label="Number"
        value={number}
        onChange={(e) => setNumber(e.target.value)}
        placeholder="15550000100"
        error={error !== undefined}
        helperText={
          error ??
          "Registered at the HSS for phones that can take SMS over IP. Empty registers none."
        }
      />
    </EditDialog>
  );
}
