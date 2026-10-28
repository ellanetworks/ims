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
} from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import QueryAlert from "@/components/QueryAlert";
import { useAuth } from "@/contexts/AuthContext";
import UserDialog from "@/components/mcptt/UserDialog";
import UserDrawer from "@/components/mcptt/UserDrawer";
import { useMCPTTLive, useMCPTTSettings } from "@/hooks/useMCPTT";
import { deleteUser, mcpttKeys, type MCPTTUser } from "@/queries/mcptt";
import {
  groupLabel,
  groupsOf,
  sameIdentity,
  shortID,
  userLabel,
} from "@/utils/mcptt";

const PAGE_SIZE_OPTIONS = [25, 50, 100];

export const gridSx = {
  bgcolor: "background.paper",
  "& .MuiDataGrid-row": { cursor: "pointer" },
  "& .MuiDataGrid-cell": {
    display: "flex",
    alignItems: "center",
    py: 1,
    overflowWrap: "anywhere",
  },
};

// NameCell is a name above its identity, when the name is not the identity's short form.
export function NameCell({ name, id }: { name: string; id: string }) {
  return (
    <Stack sx={{ minWidth: 0 }}>
      <span>{name}</span>
      {name !== id && (
        <Typography variant="body2" color="text.secondary">
          {id}
        </Typography>
      )}
    </Stack>
  );
}

// matches reports whether a user matches a search: of its name, MCPTT ID or IMS identities.
const matches = (u: MCPTTUser, search: string) => {
  const s = search.toLowerCase();
  return [u.alias, u.mcptt_id, ...u.impus].some((v) =>
    v.toLowerCase().includes(s),
  );
};

export function UsersTab() {
  const queryClient = useQueryClient();
  const editable = useAuth().can("mcptt:update");
  const { users, groups, domain, homeDomain } = useMCPTTSettings();
  const { registrations, affiliations } = useMCPTTLive();
  const [search, setSearch] = useState("");
  const [dialog, setDialog] = useState<{ user?: MCPTTUser } | null>(null);
  const [deleting, setDeleting] = useState<MCPTTUser | null>(null);
  const [selected, setSelected] = useState<string | null>(null);

  const deletion = useMutation({
    mutationFn: (u: MCPTTUser) => deleteUser(u.mcptt_id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.users });
      void queryClient.invalidateQueries({ queryKey: mcpttKeys.groups });
      setDeleting(null);
    },
  });

  const allGroups = useMemo(() => groups.data ?? [], [groups.data]);
  const regs = useMemo(() => registrations.data ?? [], [registrations.data]);
  const rows = (users.data ?? []).filter(
    (u) => search.trim() === "" || matches(u, search.trim()),
  );
  const shown = selected
    ? (users.data?.find((u) => u.mcptt_id === selected) ?? null)
    : null;

  const allColumns: GridColDef<MCPTTUser>[] = [
    {
      field: "name",
      headerName: "Name",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) => (
        <NameCell
          name={userLabel(row, domain)}
          id={shortID(row.mcptt_id, domain)}
        />
      ),
    },
    {
      field: "impus",
      headerName: "IMS Identity",
      flex: 1,
      minWidth: 200,
      renderCell: ({ row }) => (
        <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
          <span>{row.impus[0]?.replace(/^sips?:/, "") ?? "—"}</span>
          {row.impus.length > 1 && (
            <Typography variant="body2" color="text.secondary">
              +{row.impus.length - 1}
            </Typography>
          )}
        </Stack>
      ),
    },
    {
      field: "groups",
      headerName: "Groups",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) => {
        const member = groupsOf(allGroups, row.mcptt_id);
        return member.length === 0 ? (
          "—"
        ) : (
          <Stack direction="row" sx={{ gap: 0.5, flexWrap: "wrap" }}>
            {member.map((g) => (
              <Chip
                key={g.group_id}
                size="small"
                variant="outlined"
                label={groupLabel(g, domain)}
              />
            ))}
          </Stack>
        );
      },
    },
    {
      field: "phones",
      headerName: "Phones",
      width: 130,
      renderCell: ({ row }) => {
        const n = regs.filter((r) =>
          sameIdentity(r.mcptt_id, row.mcptt_id),
        ).length;
        return n > 0 ? (
          <Chip size="small" color="success" label={`${n} registered`} />
        ) : (
          <Chip size="small" variant="outlined" label="None" />
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
            aria-label={`Edit ${userLabel(row, domain)}`}
            onClick={(e) => {
              e.stopPropagation();
              setDialog({ user: row });
            }}
          >
            <EditIcon fontSize="small" color="primary" />
          </IconButton>
          <IconButton
            size="small"
            aria-label={`Delete ${userLabel(row, domain)}`}
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
  const columns = editable
    ? allColumns
    : allColumns.filter((c) => c.field !== "actions");

  const leaving = deleting ? groupsOf(allGroups, deleting.mcptt_id) : [];

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
          placeholder="Name, MCPTT ID or IMS identity"
          sx={{ minWidth: 280 }}
        />
        {editable && (
          <Button
            variant="contained"
            color="success"
            startIcon={<AddIcon />}
            onClick={() => setDialog({})}
            disabled={users.data === undefined || groups.data === undefined}
          >
            Add User
          </Button>
        )}
      </Stack>
      <QueryAlert
        error={users.error ?? groups.error}
        hasData={users.data !== undefined}
        subject="users"
      />
      <DataGrid<MCPTTUser>
        aria-label="Users"
        rows={rows}
        columns={columns}
        getRowId={(row) => row.mcptt_id}
        getRowHeight={() => "auto"}
        loading={users.isPending}
        initialState={{ pagination: { paginationModel: { pageSize: 25 } } }}
        pageSizeOptions={PAGE_SIZE_OPTIONS}
        onRowClick={({ row }) => setSelected(row.mcptt_id)}
        onCellKeyDown={({ row }, event) => {
          if (event.key === "Enter") setSelected(row.mcptt_id);
        }}
        disableColumnFilter
        disableColumnMenu
        disableRowSelectionOnClick
        disableVirtualization
        autoHeight
        localeText={{ noRowsLabel: search ? "No user matches." : "No users." }}
        sx={gridSx}
      />
      {dialog && (
        <UserDialog
          user={dialog.user}
          groups={allGroups}
          domain={domain}
          homeDomain={homeDomain}
          onClose={() => setDialog(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${userLabel(deleting, domain)}?`}
          action="Delete"
          pending={deletion.isPending}
          error={deletion.error}
          onConfirm={() => deletion.mutate(deleting)}
          onClose={() => setDeleting(null)}
        >
          <Typography>
            {leaving.length > 0
              ? `The user also leaves ${leaving.map((g) => groupLabel(g, domain)).join(", ")}. `
              : ""}
            Its phones leave their calls and can no longer use push-to-talk.
          </Typography>
        </ConfirmDialog>
      )}
      <UserDrawer
        user={shown}
        groups={allGroups}
        registrations={regs}
        affiliations={affiliations.data ?? []}
        domain={domain}
        onClose={() => setSelected(null)}
      />
    </>
  );
}
