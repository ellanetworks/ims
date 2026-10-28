import { useState } from "react";
import { Alert, Collapse } from "@mui/material";

// DialogError is why a dialog's action failed, as in Ella Core: above the form, until dismissed or the next failure.
export default function DialogError({
  error,
  prefix,
}: {
  error: Error | null;
  prefix: string;
}) {
  const [dismissed, setDismissed] = useState<Error | null>(null);
  const shown = error !== null && error !== dismissed;

  return (
    <Collapse in={shown} unmountOnExit>
      <Alert severity="error" onClose={() => setDismissed(error)}>
        {prefix}: {error?.message}
      </Alert>
    </Collapse>
  );
}
