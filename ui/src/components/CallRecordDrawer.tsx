import type { ReactNode } from "react";
import {
  Box,
  Divider,
  Drawer,
  IconButton,
  Stack,
  Typography,
} from "@mui/material";
import { Close as CloseIcon } from "@mui/icons-material";
import CallOutcomeChip from "@/components/CallOutcomeChip";
import CopyButton from "@/components/CopyButton";
import Fields from "@/components/Fields";
import type { CallRecord } from "@/queries/callRecords";
import {
  callerOf,
  calleeOf,
  endedByLabels,
  formatDuration,
} from "@/utils/callRecords";
import { formatTimestamp } from "@/utils/dates";

const lines = (values: string[]) =>
  values.length > 0 ? (
    <Box component="span" sx={{ display: "flex", flexDirection: "column" }}>
      {values.map((v) => (
        <span key={v}>{v}</span>
      ))}
    </Box>
  ) : (
    "—"
  );

const time = (iso?: string) => (iso ? formatTimestamp(iso) : "—");

function CallRecordDetail({
  record,
  onClose,
}: {
  record: CallRecord;
  onClose: () => void;
}) {
  const call: [string, ReactNode][] = [
    ["Outcome", <CallOutcomeChip key="outcome" record={record} />],
    ["SIP Status", record.sip_status ?? "—"],
    ["Ended By", record.ended_by ? endedByLabels[record.ended_by] : "—"],
    ["Rang", record.alerted ? "yes" : "no"],
    ["Media", record.media.join(", ") || "—"],
    [
      "Duration",
      record.duration_ms !== undefined
        ? formatDuration(record.duration_ms)
        : "—",
    ],
  ];

  const parties: [string, ReactNode][] = [
    ["Calling Party", lines(record.calling_party)],
    ["Caller IMPI", record.caller_impi ?? "—"],
    ["Dialled", record.requested_party],
    ["Called Party", record.called_party ?? "—"],
    ["Callee IMPI", record.callee_impi ?? "—"],
  ];

  const times: [string, ReactNode][] = [
    ["Requested", time(record.requested_at)],
    [
      record.outcome === "answered" ? "Answered" : "Final Response",
      time(record.delivery_start_at),
    ],
    ["Ended", time(record.delivery_end_at)],
  ];

  const ids: [string, ReactNode][] = [
    [
      "ICID",
      <Stack key="icid" direction="row" sx={{ alignItems: "center", gap: 0.5 }}>
        <span>{record.icid}</span>
        <CopyButton value={record.icid} label="ICID" />
      </Stack>,
    ],
    ["Call-ID", record.session_id],
  ];

  return (
    <Stack spacing={2} sx={{ p: 2 }}>
      <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>
        <Typography
          id="call-record-drawer-title"
          variant="h6"
          component="h2"
          sx={{ flexGrow: 1, overflowWrap: "anywhere" }}
        >
          {callerOf(record)} → {calleeOf(record)}
        </Typography>
        <IconButton aria-label="Close" onClick={onClose}>
          <CloseIcon />
        </IconButton>
      </Stack>
      <Fields rows={call} />
      <Divider />
      <Fields rows={parties} />
      <Divider />
      <Fields rows={times} />
      <Divider />
      <Fields rows={ids} />
    </Stack>
  );
}

export default function CallRecordDrawer({
  record,
  onClose,
}: {
  record: CallRecord | null;
  onClose: () => void;
}) {
  return (
    <Drawer
      anchor="right"
      open={record !== null}
      onClose={onClose}
      sx={{ zIndex: (theme) => theme.zIndex.modal }}
      slotProps={{
        paper: {
          sx: { width: { xs: "100%", sm: 520 } },
          "aria-labelledby": "call-record-drawer-title",
        },
      }}
    >
      {record && (
        <CallRecordDetail key={record.id} record={record} onClose={onClose} />
      )}
    </Drawer>
  );
}
