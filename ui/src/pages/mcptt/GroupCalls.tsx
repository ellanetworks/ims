import { useEffect, useState } from "react";
import {
  Box,
  Chip,
  Divider,
  Drawer,
  IconButton,
  Stack,
  Typography,
} from "@mui/material";
import { Close as CloseIcon, Mic as MicIcon } from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import DrawerSection from "@/components/DrawerSection";
import Fields from "@/components/Fields";
import MCPTTGate from "@/components/MCPTTGate";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { useMCPTTLive, useMCPTTSettings } from "@/hooks/useMCPTT";
import type { Group, GroupCall, MCPTTUser } from "@/queries/mcptt";
import { gridSx } from "@/pages/mcptt/Users";
import { formatTimestamp } from "@/utils/dates";
import {
  formatElapsed,
  groupLabel,
  sameIdentity,
  shortID,
  userLabel,
} from "@/utils/mcptt";

// useNow is the time, every second: the elapsed times tick between the refreshes.
function useNow() {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  return now;
}

interface Names {
  user: (id: string) => string;
  group: (id: string) => string;
}

const namesOf = (
  users: MCPTTUser[],
  groups: Group[],
  domain: string,
): Names => ({
  user: (id) => {
    const u = users.find((x) => sameIdentity(x.mcptt_id, id));
    return u ? userLabel(u, domain) : shortID(id, domain);
  },
  group: (id) => {
    const g = groups.find((x) => sameIdentity(x.group_id, id));
    return g ? groupLabel(g, domain) : shortID(id, domain);
  },
});

function CallDrawer({
  call,
  names,
  now,
  onClose,
}: {
  call: GroupCall | null;
  names: Names;
  now: number;
  onClose: () => void;
}) {
  return (
    <Drawer
      anchor="right"
      open={call !== null}
      onClose={onClose}
      sx={{ zIndex: (theme) => theme.zIndex.modal }}
      slotProps={{
        paper: {
          sx: { width: { xs: "100%", sm: 520 } },
          "aria-labelledby": "call-drawer-title",
        },
      }}
    >
      {call && (
        <Stack
          spacing={3}
          sx={{ p: 3, "& dl": { gridTemplateColumns: "8rem 1fr" } }}
        >
          <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>
            <Typography
              id="call-drawer-title"
              variant="h5"
              component="h2"
              sx={{ flexGrow: 1 }}
            >
              {names.group(call.group_id)}
            </Typography>
            <IconButton aria-label="Close" onClick={onClose}>
              <CloseIcon />
            </IconButton>
          </Stack>
          <Divider />
          <DrawerSection id="call-title" title="Call">
            <Fields
              rows={[
                [
                  "Talking",
                  call.talker
                    ? `${names.user(call.talker.mcptt_id)} (${formatElapsed(call.talker.since, now)})`
                    : "Nobody",
                ],
                ["Started By", names.user(call.originator)],
                [
                  "Started",
                  call.started_at
                    ? formatTimestamp(call.started_at)
                    : "Setting up",
                ],
                ["Ends By", call.ends_at ? formatTimestamp(call.ends_at) : "—"],
                ["Session", call.session_id],
              ]}
            />
          </DrawerSection>
          <DrawerSection
            id="call-participants-title"
            title={`Participants (${call.participants.length})`}
          >
            <Stack
              component="ul"
              spacing={1}
              sx={{ m: 0, p: 0, listStyle: "none" }}
            >
              {call.participants.map((p) => (
                <Stack
                  key={p.mcptt_id}
                  component="li"
                  direction="row"
                  sx={{ alignItems: "center", gap: 1 }}
                >
                  <Box sx={{ flexGrow: 1 }}>{names.user(p.mcptt_id)}</Box>
                  {call.talker &&
                    sameIdentity(call.talker.mcptt_id, p.mcptt_id) && (
                      <Chip
                        size="small"
                        color="success"
                        icon={<MicIcon />}
                        label="Talking"
                      />
                    )}
                  {p.recvonly && (
                    <Chip size="small" variant="outlined" label="Listen only" />
                  )}
                  {p.required && (
                    <Chip size="small" variant="outlined" label="Required" />
                  )}
                  <Chip
                    size="small"
                    variant={p.state === "joined" ? "filled" : "outlined"}
                    label={p.state === "joined" ? "Joined" : "Invited"}
                  />
                </Stack>
              ))}
            </Stack>
          </DrawerSection>
        </Stack>
      )}
    </Drawer>
  );
}

function CallsContent() {
  const { users, groups, domain } = useMCPTTSettings();
  const { calls } = useMCPTTLive();
  const [selected, setSelected] = useState<string | null>(null);
  const now = useNow();
  const names = namesOf(users.data ?? [], groups.data ?? [], domain);
  const shown = selected
    ? (calls.data?.find((c) => c.id === selected) ?? null)
    : null;

  const columns: GridColDef<GroupCall>[] = [
    {
      field: "group",
      headerName: "Group",
      flex: 1,
      minWidth: 160,
      valueGetter: (_v, row) => names.group(row.group_id),
    },
    {
      field: "talker",
      headerName: "Talking",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) =>
        row.talker ? (
          <Chip
            size="small"
            color="success"
            icon={<MicIcon />}
            label={`${names.user(row.talker.mcptt_id)} · ${formatElapsed(row.talker.since, now)}`}
          />
        ) : (
          <Typography variant="body2" color="text.secondary">
            Nobody
          </Typography>
        ),
    },
    {
      field: "participants",
      headerName: "Joined",
      width: 110,
      valueGetter: (_v, row) =>
        `${row.participants.filter((p) => p.state === "joined").length} of ${row.participants.length}`,
    },
    {
      field: "originator",
      headerName: "Started By",
      flex: 1,
      minWidth: 140,
      valueGetter: (_v, row) => names.user(row.originator),
    },
    {
      field: "duration",
      headerName: "Duration",
      width: 120,
      valueGetter: (_v, row) =>
        row.started_at ? formatElapsed(row.started_at, now) : "Setting up",
    },
  ];

  return (
    <>
      <QueryAlert
        error={calls.error}
        hasData={calls.data !== undefined}
        subject="group calls"
      />
      <DataGrid<GroupCall>
        aria-label="Group calls"
        rows={calls.data ?? []}
        columns={columns}
        getRowId={(row) => row.id}
        getRowHeight={() => "auto"}
        loading={calls.isPending}
        initialState={{ pagination: { paginationModel: { pageSize: 25 } } }}
        pageSizeOptions={[25, 50, 100]}
        onRowClick={({ row }) => setSelected(row.id)}
        onCellKeyDown={({ row }, event) => {
          if (event.key === "Enter") setSelected(row.id);
        }}
        disableColumnFilter
        disableColumnMenu
        disableRowSelectionOnClick
        disableVirtualization
        autoHeight
        localeText={{ noRowsLabel: "No group call in progress." }}
        sx={gridSx}
      />
      <CallDrawer
        call={shown}
        names={names}
        now={now}
        onClose={() => setSelected(null)}
      />
    </>
  );
}

export default function MCPTTGroupCalls() {
  const { calls } = useMCPTTLive(false);

  return (
    <Box component="section" aria-labelledby="mcptt-calls-title">
      <PageHeader
        id="mcptt-calls-title"
        title="Group Calls"
        count={calls.data?.length}
        description="The push-to-talk calls in progress, and who is talking."
      />
      <MCPTTGate>{() => <CallsContent />}</MCPTTGate>
    </Box>
  );
}
