import { Alert } from "@mui/material";
import ErrorAlert from "@/components/ErrorAlert";

// QueryAlert is the state of a query that the page cannot show, as Ella Core's QueryState: offline, a failed first
// load with a retry, or a failed refresh, which keeps the last data on screen.
export default function QueryAlert({
  query,
  subject,
}: {
  query: {
    error: Error | null;
    data: unknown;
    fetchStatus: string;
    isFetching: boolean;
    refetch: () => unknown;
  };
  subject: string;
}) {
  if (query.fetchStatus === "paused") {
    return (
      <Alert severity="warning" sx={{ mb: 2 }}>
        You appear to be offline. The {subject} cannot be updated right now.
      </Alert>
    );
  }

  if (!query.error) return null;

  if (query.data !== undefined) {
    return (
      <Alert severity="warning" sx={{ mb: 2 }}>
        Showing the last loaded {subject}. Refresh failed: {query.error.message}
      </Alert>
    );
  }

  return (
    <ErrorAlert
      resource={subject}
      error={query.error}
      onRetry={() => void query.refetch()}
      retrying={query.isFetching}
    />
  );
}
