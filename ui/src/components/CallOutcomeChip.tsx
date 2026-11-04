import { Chip, type ChipProps } from "@mui/material";
import type { CallOutcome, CallRecord } from "@/queries/callRecords";
import { outcomeLabels } from "@/utils/callRecords";

export const outcomeColors: Record<CallOutcome, ChipProps["color"]> = {
  answered: "success",
  cancelled: "default",
  busy: "default",
  rejected: "default",
  no_answer: "default",
  unavailable: "default",
  failed: "error",
};

export function OutcomeChip({ outcome }: { outcome: CallOutcome }) {
  return (
    <Chip
      label={outcomeLabels[outcome]}
      color={outcomeColors[outcome]}
      size="small"
    />
  );
}

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

  return record.outcome ? <OutcomeChip outcome={record.outcome} /> : <>—</>;
}
