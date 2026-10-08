import { useState } from "react";
import {
  Alert,
  FormControl,
  FormControlLabel,
  FormLabel,
  Radio,
  RadioGroup,
  TextField,
} from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  type Policy,
  type PolicyInterface,
  updatePolicy,
} from "@/queries/policy";
import { policyLabels } from "@/utils/labels";
import { RESTART_WARNING } from "@/utils/restart";

const INTERFACES: PolicyInterface[] = ["none", "rx", "n5"];

export default function EditPolicyDialog({
  policy,
  onClose,
}: {
  policy: Policy;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [policyInterface, setPolicyInterface] = useState(policy.interface);
  const [pcfURI, setPCFURI] = useState(policy.n5?.pcf_uri ?? "");

  const mutation = useMutation({
    mutationFn: updatePolicy,
    onSuccess: (updated) => {
      queryClient.setQueryData(["policy"], updated);
      onClose();
    },
  });

  const next: Policy =
    policyInterface === "n5"
      ? { interface: "n5", n5: { pcf_uri: pcfURI.trim() } }
      : { interface: policyInterface };

  const uriError =
    policyInterface !== "n5" || /^https?:\/\/[^/]+/.test(next.n5!.pcf_uri)
      ? undefined
      : "http[s]://host[:port][/prefix]";
  const changed =
    next.interface !== policy.interface ||
    next.n5?.pcf_uri !== policy.n5?.pcf_uri;

  return (
    <EditDialog
      title="Edit Voice QoS"
      valid={uriError === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate(next)}
      onClose={onClose}
    >
      <FormControl>
        <FormLabel id="policy-function-label">Policy Function</FormLabel>
        <RadioGroup
          row
          aria-labelledby="policy-function-label"
          value={policyInterface}
          onChange={(e) =>
            setPolicyInterface(e.target.value as PolicyInterface)
          }
        >
          {INTERFACES.map((i) => (
            <FormControlLabel
              key={i}
              value={i}
              control={<Radio />}
              label={policyLabels[i]}
            />
          ))}
        </RadioGroup>
      </FormControl>
      {policyInterface === "n5" && (
        <TextField
          label="PCF URI"
          value={pcfURI}
          onChange={(e) => setPCFURI(e.target.value)}
          error={pcfURI !== "" && uriError !== undefined}
          helperText={uriError}
          placeholder="https://pcf.example.org:7777"
          required
        />
      )}
      {changed && uriError === undefined && (
        <Alert severity="warning">{RESTART_WARNING}</Alert>
      )}
    </EditDialog>
  );
}
