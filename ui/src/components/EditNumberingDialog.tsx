import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { type Operator, updateOperator } from "@/queries/operator";

export default function EditNumberingDialog({
  operator,
  onClose,
}: {
  operator: Operator;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [numbering, setNumbering] = useState({
    country_code: operator.numbering.country_code,
    national_prefix: operator.numbering.national_prefix,
    international_prefix: operator.numbering.international_prefix,
  });
  const [minDigits, setMinDigits] = useState(
    String(operator.numbering.min_national_digits),
  );

  const mutation = useMutation({
    mutationFn: updateOperator,
    onSuccess: (updated) => {
      queryClient.setQueryData(["operator"], updated);
      showSnackbar("Numbering plan updated successfully.", "success");
      onClose();
    },
  });

  const prefixError = (value: string) =>
    /^\d{0,4}$/.test(value) ? undefined : "Up to 4 digits";

  const errors = {
    country_code: /^[1-9]\d{0,2}$/.test(numbering.country_code)
      ? undefined
      : "1 to 3 digits, not starting with 0",
    national_prefix: prefixError(numbering.national_prefix),
    international_prefix:
      prefixError(numbering.international_prefix) ??
      (numbering.international_prefix !== "" &&
      numbering.international_prefix === numbering.national_prefix
        ? "Must differ from the national prefix"
        : undefined),
  };

  const minDigitsError =
    /^\d{1,2}$/.test(minDigits) && Number(minDigits) <= 15
      ? undefined
      : "0 to 15";

  const valid =
    minDigitsError === undefined &&
    Object.values(errors).every((e) => e === undefined);

  const field = (name: keyof typeof numbering) => ({
    value: numbering[name],
    onChange: (e: { target: { value: string } }) =>
      setNumbering((n) => ({ ...n, [name]: e.target.value.trim() })),
    error: errors[name] !== undefined,
    helperText: errors[name],
  });

  return (
    <EditDialog
      title="Edit Numbering"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({
          ...operator,
          numbering: { ...numbering, min_national_digits: Number(minDigits) },
        })
      }
      onClose={onClose}
    >
      <TextField label="Country Code" {...field("country_code")} required />
      <TextField label="National Prefix" {...field("national_prefix")} />
      <TextField
        label="International Prefix"
        {...field("international_prefix")}
      />
      <TextField
        label="Minimum National Digits"
        value={minDigits}
        onChange={(e) => setMinDigits(e.target.value.trim())}
        error={minDigitsError !== undefined}
        helperText={minDigitsError}
        required
      />
    </EditDialog>
  );
}
