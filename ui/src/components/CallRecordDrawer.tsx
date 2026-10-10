import type { ReactNode } from "react";
import { Drawer, IconButton, Stack, Typography } from "@mui/material";
import { Close as CloseIcon } from "@mui/icons-material";
import CallOutcomeChip from "@/components/CallOutcomeChip";
import CopyButton from "@/components/CopyButton";
import DrawerSection from "@/components/DrawerSection";
import Fields from "@/components/Fields";
import type { CallRecord } from "@/queries/callRecords";
import {
  callerOf,
  calleeOf,
  endedByLabels,
  formatDuration,
  dialledOf,
} from "@/utils/callRecords";
import { formatClock, formatDate, formatTimestamp } from "@/utils/dates";
import { mediaText } from "@/utils/labels";
import { subscriberOf } from "@/utils/registrations";

// identityRow is a party's private identity: its IMSI when derived from one, else the IMPI.
const identityRow = (party: string, impi?: string): [string, ReactNode] => {
  const subscriber = impi && subscriberOf(impi);
  return [
    `${party} ${subscriber && subscriber === impi ? "IMPI" : "IMSI"}`,
    subscriber ?? "—",
  ];
};

function Copyable({ value, label }: { value: string; label: string }) {
  return (
    <Stack direction="row" sx={{ alignItems: "center", gap: 0.5 }}>
      <span>{value}</span>
      <CopyButton value={value} label={label} />
    </Stack>
  );
}

function CallRecordDetail({
  record,
  onClose,
}: {
  record: CallRecord;
  onClose: () => void;
}) {
  const answered = record.outcome === "answered";

  const call: [string, ReactNode][] = [
    ["Outcome", <CallOutcomeChip key="outcome" record={record} />],
  ];
  // The final status tells why a call was not answered; for an answered call, it is 200.
  if (!answered && record.sip_status !== undefined) {
    call.push(["SIP Status", String(record.sip_status)]);
  }
  call.push(
    ["Ended By", record.ended_by ? endedByLabels[record.ended_by] : "—"],
    ["Rang", record.alerted ? "Yes" : "No"],
    ["Media", mediaText(record.media)],
    [
      "Duration",
      record.duration_ms !== undefined
        ? formatDuration(record.duration_ms)
        : "—",
    ],
  );

  const callee = calleeOf(record);
  const dialled = dialledOf(record.requested_party);
  const parties: [string, ReactNode][] = [
    ["Caller", callerOf(record)],
    identityRow("Caller", record.caller_impi),
    ["Callee", callee],
  ];
  // What the caller dialled, when the IMS routed the call elsewhere, such as a local number made international.
  if (dialled !== callee) {
    parties.push(["Dialled", dialled]);
  }
  parties.push(identityRow("Callee", record.callee_impi));

  // The date shows once; a time on another day shows in full.
  const day = formatDate(record.requested_at);
  const at = (iso?: string) =>
    !iso
      ? "—"
      : formatDate(iso) === day
        ? formatClock(iso)
        : formatTimestamp(iso);
  const timeline: [string, ReactNode][] = [
    ["Date", day],
    ["Requested", at(record.requested_at)],
    [answered ? "Answered" : "Final Response", at(record.delivery_start_at)],
    ["Ended", at(record.delivery_end_at)],
  ];

  const ids: [string, ReactNode][] = [
    ["ICID", <Copyable key="icid" value={record.icid} label="ICID" />],
    [
      "Call-ID",
      <Copyable key="call-id" value={record.session_id} label="Call-ID" />,
    ],
  ];

  return (
    // One key column width for all sections, so that their values line up.
    <Stack
      spacing={3}
      sx={{ p: 3, "& dl": { gridTemplateColumns: "8.5rem 1fr" } }}
    >
      <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>
        <Typography
          id="call-record-drawer-title"
          variant="h5"
          component="h2"
          sx={{ flexGrow: 1, overflowWrap: "anywhere" }}
        >
          {callerOf(record)} → {callee}
        </Typography>
        <IconButton aria-label="Close" onClick={onClose}>
          <CloseIcon />
        </IconButton>
      </Stack>
      <DrawerSection id="call-title" title="Call">
        <Fields rows={call} />
      </DrawerSection>
      <DrawerSection id="parties-title" title="Parties">
        <Fields rows={parties} />
      </DrawerSection>
      <DrawerSection id="timeline-title" title="Timeline">
        <Fields rows={timeline} />
      </DrawerSection>
      <DrawerSection id="identifiers-title" title="Identifiers">
        <Fields rows={ids} />
      </DrawerSection>
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
