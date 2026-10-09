import { Chip } from "@mui/material";
import type { CallRecord } from "@/queries/callRecords";
import { outcomeLabels } from "@/utils/callRecords";

// CallOutcomeChip is how a call ended, or that it is in progress, or that the IMS lost it.
export default function CallOutcomeChip({ record }: { record: CallRecord }) {
  if (record.incomplete) {
    return <Chip label="incomplete" size="small" variant="outlined" />;
  }

  if (record.in_progress) {
    const label =
      record.outcome === "answered"
        ? "in call"
        : record.alerted
          ? "ringing"
          : "calling";

    return <Chip label={label} color="info" size="small" />;
  }

  switch (record.outcome) {
    case "answered":
      return (
        <Chip label={outcomeLabels.answered} color="success" size="small" />
      );
    case "failed":
      return <Chip label={outcomeLabels.failed} color="error" size="small" />;
    case undefined:
      return <>—</>;
    default:
      return (
        <Chip
          label={outcomeLabels[record.outcome]}
          color="warning"
          size="small"
        />
      );
  }
}
