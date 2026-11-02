import { useState } from "react";
import { Alert, Collapse } from "@mui/material";

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
