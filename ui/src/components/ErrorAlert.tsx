import { useState } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Collapse,
  IconButton,
  Link,
  Typography,
} from "@mui/material";
import { ContentCopy as ContentCopyIcon } from "@mui/icons-material";
import { useCopyToClipboard } from "@/hooks/useCopyToClipboard";
import { ApiError } from "@/queries/utils";

const primaryMessage = (error: unknown): string =>
  error instanceof Error ? error.message : "An unexpected error occurred.";

const technicalDetail = (error: unknown): string | undefined => {
  if (error instanceof ApiError) return error.detail;
  if (error instanceof Error) return error.stack;
  return undefined;
};

// ErrorAlert is a failure to load a resource, as in Ella Core: what happened, a retry, and the technical details on
// demand.
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
  const copy = useCopyToClipboard();

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
              <IconButton
                size="small"
                onClick={() => void copy(detail, "Error details")}
                aria-label={`Copy ${resource} error details`}
              >
                <ContentCopyIcon fontSize="inherit" />
              </IconButton>
            </Box>
          </Collapse>
        </Box>
      )}
    </Alert>
  );
}
