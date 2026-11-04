import { useState } from "react";
import { Box, Button, Link } from "@mui/material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import { Link as RouterLink } from "react-router-dom";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { signInMethods } from "@/components/UserCards";
import { CreateUserDialog } from "@/components/UserDialogs";
import { getOIDC, roleFromGroups, type OIDCSettings } from "@/queries/oidc";
import { listUsers, roleLabels, type User } from "@/queries/users";
import { GRID_SX } from "@/utils/layout";

const roleSource = (user: User, oidc?: OIDCSettings) => {
  if (user.role_pinned) return " (pinned)";
  if (roleFromGroups(user, oidc)) return " (from groups)";
  return "";
};

const columns = (oidc?: OIDCSettings): GridColDef<User>[] => [
  {
    field: "email",
    headerName: "Email",
    flex: 1,
    minWidth: 220,
    renderCell: ({ row }) => (
      <Link
        component={RouterLink}
        to={`/users/${encodeURIComponent(row.email)}`}
        variant="body2"
        sx={{ color: "link", textDecoration: "underline" }}
      >
        {row.email}
      </Link>
    ),
  },
  {
    field: "role_id",
    headerName: "Role",
    width: 240,
    valueGetter: (_value, row) =>
      roleLabels[row.role_id] + roleSource(row, oidc),
  },
  {
    field: "sign_in",
    headerName: "Sign-in",
    flex: 1,
    minWidth: 280,
    valueGetter: (_value, row) =>
      signInMethods(row).join(", ") || "Single sign-on, not linked yet",
  },
];

export default function Users() {
  const [creating, setCreating] = useState(false);
  const [pagination, setPagination] = useState<GridPaginationModel>({
    page: 0,
    pageSize: 25,
  });

  const users = useQuery({
    queryKey: ["users", pagination],
    queryFn: () =>
      listUsers({ page: pagination.page + 1, perPage: pagination.pageSize }),
    placeholderData: (prev) => prev,
  });
  const { data, isFetching } = users;
  const oidc = useQuery({ queryKey: ["oidc"], queryFn: getOIDC });

  return (
    <Box component="section" aria-labelledby="users-title">
      <PageHeader
        id="users-title"
        title="Users"
        count={data?.total_count}
        description="The people and programs that use the UI and the API, and what each role may do."
        action={
          <Button
            variant="contained"
            color="success"
            onClick={() => setCreating(true)}
          >
            Create
          </Button>
        }
      />
      <QueryAlert query={users} subject="users" />
      <DataGrid
        aria-label="Users"
        rows={data?.items ?? []}
        columns={columns(oidc.data)}
        getRowId={(row) => row.email}
        loading={isFetching && !data}
        paginationMode="server"
        rowCount={data?.total_count ?? 0}
        paginationModel={pagination}
        onPaginationModelChange={setPagination}
        pageSizeOptions={[25, 50]}
        disableColumnMenu
        disableColumnSorting
        disableRowSelectionOnClick
        disableVirtualization
        autoHeight
        localeText={{ noRowsLabel: "No users." }}
        sx={GRID_SX}
      />
      {creating && <CreateUserDialog onClose={() => setCreating(false)} />}
    </Box>
  );
}
