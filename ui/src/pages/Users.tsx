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
import { CreateUserDialog } from "@/components/UserDialogs";
import { listUsers, roleLabels, type User } from "@/queries/users";

const columns: GridColDef<User>[] = [
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
      >
        {row.email}
      </Link>
    ),
  },
  {
    field: "role_id",
    headerName: "Role",
    width: 180,
    valueGetter: (_value, row) => roleLabels[row.role_id],
  },
];

const gridSx = {
  width: "100%",
  border: 1,
  borderColor: "divider",
  bgcolor: "background.paper",
};

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
      <QueryAlert
        error={users.error}
        hasData={data !== undefined}
        subject="users"
      />
      <DataGrid
        aria-label="Users"
        rows={data?.items ?? []}
        columns={columns}
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
        sx={gridSx}
      />
      {creating && <CreateUserDialog onClose={() => setCreating(false)} />}
    </Box>
  );
}
