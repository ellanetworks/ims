import type { ReactNode } from "react";
import { Alert, AlertTitle, Box, Skeleton, Typography } from "@mui/material";
import { useMCPTTStatus } from "@/hooks/useMCPTT";
import type { MCPTTStatus } from "@/queries/mcptt";

const CONFIG = `mcptt:
  media:
    address: 192.0.2.10`;

// MCPTTGate shows its children, of the status of the MCPTT AS, when the IMS runs one, and how to turn it on
// otherwise.
export default function MCPTTGate({
  children,
}: {
  children: (status: MCPTTStatus) => ReactNode;
}) {
  const { status, error } = useMCPTTStatus();

  if (status === undefined) {
    return error ? (
      <Alert severity="error">Could not load the status: {error.message}</Alert>
    ) : (
      <Skeleton variant="rounded" height={160} />
    );
  }

  if (status === null) {
    return (
      <Alert severity="info">
        <AlertTitle>Push-to-talk is off</AlertTitle>
        <Typography variant="body2">
          To run the built-in MCPTT server, add an <code>mcptt</code> section to{" "}
          <code>ims.yaml</code> with the address phones reach its media on, then
          restart Ella IMS:
        </Typography>
        <Box
          component="pre"
          sx={{ mt: 1, mb: 0, p: 1, bgcolor: "action.hover", borderRadius: 1 }}
        >
          {CONFIG}
        </Box>
      </Alert>
    );
  }

  return children(status);
}
