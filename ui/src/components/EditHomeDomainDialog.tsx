import { useState } from "react";
import { Alert, TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { type Operator, updateOperator } from "@/queries/operator";
import { derivedDomain, domainError, homeDomain } from "@/utils/operator";
import { RESTART_WARNING } from "@/utils/restart";

export default function EditHomeDomainDialog({
  operator,
  onClose,
}: {
  operator: Operator;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [domain, setDomain] = useState(operator.domain);

  const mutation = useMutation({
    mutationFn: updateOperator,
    onSuccess: (updated) => {
      queryClient.setQueryData(["operator"], updated);
      void queryClient.invalidateQueries({ queryKey: ["sip"] });
      void queryClient.invalidateQueries({ queryKey: ["mcptt"] });
      void queryClient.invalidateQueries({ queryKey: ["ip-sm-gw"] });
      showSnackbar("Home network domain updated successfully.", "success");
      onClose();
    },
  });

  const derived = derivedDomain(operator.mcc, operator.mnc);
  const error = domainError(domain, derived);
  const next = homeDomain({ ...operator, domain });
  const renamed = error === undefined && next !== homeDomain(operator);

  return (
    <EditDialog
      title="Edit Home Network Domain"
      valid={error === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ ...operator, domain })}
      onClose={onClose}
    >
      <TextField
        label="Domain"
        value={domain}
        onChange={(e) =>
          setDomain(e.target.value.trim().toLowerCase().replace(/\.$/, ""))
        }
        error={error !== undefined}
        helperText={
          error ??
          "Leave empty to use the domain derived from the Operator ID. Set it only if the ISIM of your SIMs holds another domain. It must match the domain of your HSS."
        }
        placeholder={derived}
      />
      {renamed && (
        <Alert severity="warning">
          The home network domain becomes <DomainName name={next} />, and the
          MCPTT and IP-SM-GW are renamed under it.{" "}
          {next !== derived && (
            <>
              Phones without an ISIM keep using <DomainName name={derived} />
              .{" "}
            </>
          )}
          {RESTART_WARNING}
        </Alert>
      )}
    </EditDialog>
  );
}
