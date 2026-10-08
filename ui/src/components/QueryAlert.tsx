import { Alert } from "@mui/material";

interface QueryAlertProps {
  error: Error | null;
  hasData: boolean;
  subject: string;
}

export default function QueryAlert({
  error,
  hasData,
  subject,
}: QueryAlertProps) {
  if (!error) return null;

  if (hasData) {
    return (
      <Alert severity="warning" sx={{ mb: 2 }}>
        Showing the last loaded {subject}. Refresh failed: {error.message}
      </Alert>
    );
  }

  return (
    <Alert severity="error" sx={{ mb: 2 }}>
      Could not load {subject}: {error.message}
    </Alert>
  );
}
