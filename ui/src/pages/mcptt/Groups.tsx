import { useMemo, useState } from "react";
import {
  Button,
  Chip,
  IconButton,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
  Mic as MicIcon,
} from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import QueryAlert from "@/components/QueryAlert";
import GroupDialog from "@/components/mcptt/GroupDialog";
import GroupDrawer from "@/components/mcptt/GroupDrawer";
import { useMCPTTLive, useMCPTTSettings } from "@/hooks/useMCPTT";
import { deleteGroup, type Group, mcpttKeys } from "@/queries/mcptt";
import { gridSx, NameCell } from "@/pages/mcptt/Users";
import { groupLabel, sameIdentity, shortID, userLabel } from "@/utils/mcptt";

const PAGE_SIZE_OPTIONS = [25, 50, 100];

export function GroupsTab() {
  const queryClient = useQueryClient();
  const { users, groups, domain } = useMCPTTSettings();
  const { affiliations, calls } = useMCPTTLive();
  const [search, setSearch] = useState("");
  const [dialog, setDialog] = useState<{ group?: Group } | null>(null);
  const [deleting, setDeleting] = useState<Group | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

  const deletion = useMutation({
    mutationFn: (g: Group) => deleteGroup(g.group_id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.groups });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      setDeleting(null);
    },
  });

  const allUsers = useMemo(() => users.data ?? [], [users.data]);
  const affs = affiliations.data ?? [];
  const callOf = (g: Group) =>
    calls.data?.find((c) => sameIdentity(c.group_id, g.group_id));
  const s = search.trim().toLowerCase();
  const rows = (groups.data ?? []).filter(
    (g) =>
      s === "" ||
      g.display_name.toLowerCase().includes(s) ||
      g.group_id.toLowerCase().includes(s),
  );
  const shown = selected
    ? (groups.data?.find((g) => g.group_id === selected) ?? null)
    : null;
  const nameOf = (id: string) => {
    const u = allUsers.find((x) => sameIdentity(x.mcptt_id, id));
    return u ? userLabel(u, domain) : shortID(id, domain);
  };

  const columns: GridColDef<Group>[] = [
    {
      field: "name",
      headerName: "Name",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) => (
        <NameCell
          name={groupLabel(row, domain)}
          id={shortID(row.group_id, domain)}
        />
      ),
    },
    {
      field: "members",
      headerName: "Members",
      width: 110,
      valueGetter: (_v, row) => row.members.length,
    },
    {
      field: "affiliated",
      headerName: "Affiliated",
      width: 120,
      renderCell: ({ row }) => {
        const affiliated = new Set(
          affs
            .filter(
              (a) =>
                sameIdentity(a.group_id, row.group_id) &&
                a.status !== "deaffiliating",
            )
            .map((a) => a.mcptt_id),
        );
        return `${affiliated.size} of ${row.members.length}`;
      },
    },
    {
      field: "call",
      headerName: "Call",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) => {
        const call = callOf(row);
        if (!call)
          return (
            <Typography variant="body2" color="text.secondary">
              —
            </Typography>
          );
        return (
          <Chip
            size="small"
            color="success"
            icon={<MicIcon />}
            label={call.talker ? nameOf(call.talker.mcptt_id) : "In call"}
          />
        );
      },
    },
    {
      field: "actions",
      headerName: "",
      width: 96,
      align: "right",
      sortable: false,
      renderCell: ({ row }) => (
        <>
          <IconButton
            size="small"
            aria-label={`Edit ${groupLabel(row, domain)}`}
            onClick={(e) => {
              e.stopPropagation();
              setDialog({ group: row });
            }}
          >
            <EditIcon fontSize="small" color="primary" />
          </IconButton>
          <IconButton
            size="small"
            aria-label={`Delete ${groupLabel(row, domain)}`}
            onClick={(e) => {
              e.stopPropagation();
              deletion.reset();
              setDeleting(row);
            }}
          >
            <DeleteIcon fontSize="small" color="primary" />
          </IconButton>
        </>
      ),
    },
  ];

  return (
    <>
      <Stack
        direction="row"
        sx={{
          mb: 2,
          gap: 2,
          justifyContent: "space-between",
          flexWrap: "wrap",
        }}
      >
        <TextField
          label="Search"
          size="small"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          placeholder="Name or group ID"
          sx={{ minWidth: 260 }}
        />
        <Button
          variant="contained"
          color="success"
          startIcon={<AddIcon />}
          onClick={() => setDialog({})}
          disabled={users.data === undefined}
        >
          Add Group
        </Button>
      </Stack>
      <QueryAlert
        error={groups.error ?? users.error}
        hasData={groups.data !== undefined}
        subject="groups"
      />
      <DataGrid<Group>
        aria-label="Groups"
        rows={rows}
        columns={columns}
        getRowId={(row) => row.group_id}
        getRowHeight={() => "auto"}
        loading={groups.isPending}
        initialState={{ pagination: { paginationModel: { pageSize: 25 } } }}
        pageSizeOptions={PAGE_SIZE_OPTIONS}
        onRowClick={({ row }) => setSelected(row.group_id)}
        onCellKeyDown={({ row }, event) => {
          if (event.key === "Enter") setSelected(row.group_id);
        }}
        disableColumnFilter
        disableColumnMenu
        disableRowSelectionOnClick
        disableVirtualization
        autoHeight
        localeText={{
          noRowsLabel: search ? "No group matches." : "No groups.",
        }}
        sx={gridSx}
      />
      {dialog && (
        <GroupDialog
          group={dialog.group}
          users={allUsers}
          domain={domain}
          onClose={() => setDialog(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${groupLabel(deleting, domain)}?`}
          action="Delete"
          pending={deletion.isPending}
          error={deletion.error}
          onConfirm={() => deletion.mutate(deleting)}
          onClose={() => setDeleting(null)}
        >
          <Typography>
            Its {deleting.members.length} members can no longer call it, and no
            longer affiliate to it.
          </Typography>
        </ConfirmDialog>
      )}
      <GroupDrawer
        group={shown}
        users={allUsers}
        affiliations={affs}
        call={shown ? callOf(shown) : undefined}
        domain={domain}
        onClose={() => setSelected(null)}
      />
    </>
  );
}
