import { useState } from "react";
import { InputAdornment, TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { type Operator, updateOperator } from "@/queries/operator";

export default function EditNumberingDialog({
  operator,
  onClose,
}: {
  operator: Operator;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [numbering, setNumbering] = useState(operator.numbering);

  const mutation = useMutation({
    mutationFn: updateOperator,
    onSuccess: (updated) => {
      queryClient.setQueryData(["operator"], updated);
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

  const valid = Object.values(errors).every((e) => e === undefined);

  const field = (name: keyof typeof numbering) => ({
    value: numbering[name],
    onChange: (e: { target: { value: string } }) =>
      setNumbering((n) => ({ ...n, [name]: e.target.value.trim() })),
    error: errors[name] !== undefined,
    helperText: errors[name],
  });

  return (
    <EditDialog
      title="Edit Numbering Plan"
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate({ ...operator, numbering })}
      onClose={onClose}
    >
      <TextField
        label="Country Code"
        {...field("country_code")}
        slotProps={{
          input: {
            startAdornment: <InputAdornment position="start">+</InputAdornment>,
          },
        }}
        required
      />
      <TextField label="National Prefix" {...field("national_prefix")} />
      <TextField
        label="International Prefix"
        {...field("international_prefix")}
      />
    </EditDialog>
  );
}
