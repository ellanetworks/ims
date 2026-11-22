import { useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Collapse,
  Link,
  Typography,
} from "@mui/material";
import CopyButton from "@/components/CopyButton";
import { ApiError } from "@/queries/utils";

const primaryMessage = (error: unknown): string =>
  error instanceof Error ? error.message : "An unexpected error occurred.";

const technicalDetail = (error: unknown): string | undefined => {
  if (error instanceof ApiError) return error.detail;
  if (error instanceof Error) return error.stack;
  return undefined;
};

export default function ErrorAlert({
  resource,
  error,
  onRetry,
  retrying = false,
}: {
  resource: string;
  error: unknown;
  onRetry?: () => void;
  retrying?: boolean;
}) {
  const [showDetail, setShowDetail] = useState(false);
  const detail = technicalDetail(error);

  return (
    <Alert
      severity="error"
      sx={{ mb: 2 }}
      action={
        onRetry && (
          <Button
            color="inherit"
            size="small"
            onClick={onRetry}
            disabled={retrying}
          >
            {retrying ? "Retrying…" : "Retry"}
          </Button>
        )
      }
    >
      <AlertTitle>Failed to load {resource}</AlertTitle>
      <Typography variant="body2">{primaryMessage(error)}</Typography>

      {detail && (
        <Box sx={{ mt: 1 }}>
          <Link
            component="button"
            type="button"
            variant="body2"
            underline="hover"
            onClick={() => setShowDetail((shown) => !shown)}
          >
            {showDetail ? "Hide details" : "Show details"}
          </Link>
          <Collapse in={showDetail}>
            <Box
              sx={{ mt: 1, display: "flex", alignItems: "flex-start", gap: 1 }}
            >
              <Typography
                variant="body2"
                component="pre"
                sx={{
                  m: 0,
                  whiteSpace: "pre-wrap",
                  overflowX: "auto",
                  flex: 1,
                }}
              >
                {detail}
              </Typography>
              <CopyButton value={detail} label={`${resource} error details`} />
            </Box>
          </Collapse>
        </Box>
      )}
    </Alert>
  );
}
