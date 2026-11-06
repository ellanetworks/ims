import { useState, type ReactNode } from "react";
import {
  Box,
  Button,
  IconButton,
  Skeleton,
  Stack,
  Typography,
} from "@mui/material";
import { Delete as DeleteIcon, Edit as EditIcon } from "@mui/icons-material";
import {
  DataGrid,
  type GridColDef,
  type GridValidRowModel,
} from "@mui/x-data-grid";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import SIPRouteDialog from "@/components/SIPRouteDialog";
import SIPTrunkDialog from "@/components/SIPTrunkDialog";
import TrunkStateChip from "@/components/TrunkStateChip";
import { useAuth } from "@/contexts/AuthContext";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import {
  deleteSIPRoute,
  deleteSIPTrunk,
  listSIPRoutes,
  listSIPTrunks,
  type SIPRoute,
  type SIPTrunk,
  type SIPTrunkAddress,
} from "@/queries/sipTrunks";
import { formatEndpoint } from "@/utils/addresses";
import { sipTransportLabels } from "@/utils/labels";
import { GRID_SX } from "@/utils/layout";

export const SIP_TRUNKS_REFRESH_MS = 5000;

const gridSx = {
  ...GRID_SX,
  "& .MuiDataGrid-cell": {
    ...GRID_SX["& .MuiDataGrid-cell"],
    display: "flex",
    alignItems: "center",
    py: 1,
    overflowWrap: "anywhere",
  },
};

const yesNo = (value: boolean) => (value ? "Yes" : "No");

const formatAddress = (address: SIPTrunkAddress): string => {
  const host =
    address.port === undefined
      ? address.host
      : formatEndpoint(address.host, address.port);
  return address.transport
    ? `${host} (${sipTransportLabels[address.transport]})`
    : host;
};

const routeName = (route: SIPRoute) => route.prefix || "Default";

const byPrefix = (a: SIPRoute, b: SIPRoute) =>
  a.prefix === "" || b.prefix === ""
    ? Number(a.prefix === "") - Number(b.prefix === "")
    : a.prefix.localeCompare(b.prefix);

function SectionHeader({
  id,
  title,
  first,
  children,
}: {
  id: string;
  title: string;
  first?: boolean;
  children?: ReactNode;
}) {
  return (
    <Stack
      direction="row"
      sx={{
        alignItems: "center",
        justifyContent: "space-between",
        gap: 2,
        mt: first ? 0 : 4,
        mb: 2,
      }}
    >
      <Typography id={id} variant="h6" component="h2">
        {title}
      </Typography>
      {children}
    </Stack>
  );
}

function ActionButtons({
  name,
  onEdit,
  onDelete,
}: {
  name: string;
  onEdit: () => void;
  onDelete: () => void;
}) {
  return (
    <>
      <IconButton size="small" aria-label={`Edit ${name}`} onClick={onEdit}>
        <EditIcon fontSize="small" color="primary" />
      </IconButton>
      <IconButton size="small" aria-label={`Delete ${name}`} onClick={onDelete}>
        <DeleteIcon fontSize="small" color="primary" />
      </IconButton>
    </>
  );
}

export default function SIPTrunks() {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const auth = useAuth();
  const editable = auth.can("sip_trunk:update");
  const [trunkDialog, setTrunkDialog] = useState<{ trunk?: SIPTrunk } | null>(
    null,
  );
  const [routeDialog, setRouteDialog] = useState<{ route?: SIPRoute } | null>(
    null,
  );
  const [deletingTrunk, setDeletingTrunk] = useState<SIPTrunk | null>(null);
  const [deletingRoute, setDeletingRoute] = useState<SIPRoute | null>(null);

  const trunks = useQuery({
    queryKey: ["sip-trunks"],
    queryFn: listSIPTrunks,
    refetchInterval: SIP_TRUNKS_REFRESH_MS,
  });
  const routes = useQuery({
    queryKey: ["sip-routes"],
    queryFn: listSIPRoutes,
  });

  const trunkDeletion = useMutation({
    mutationFn: (trunk: SIPTrunk) => deleteSIPTrunk(trunk.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["sip-trunks"] });
      setDeletingTrunk(null);
      showSnackbar("Trunk deleted successfully.", "success");
    },
    onError: (error) => {
      setDeletingTrunk(null);
      showSnackbar(`Failed to delete trunk: ${errorMessage(error)}`, "error");
    },
  });

  const routeDeletion = useMutation({
    mutationFn: (route: SIPRoute) => deleteSIPRoute(route.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["sip-routes"] });
      setDeletingRoute(null);
      showSnackbar("Route deleted successfully.", "success");
    },
    onError: (error) => {
      setDeletingRoute(null);
      showSnackbar(`Failed to delete route: ${errorMessage(error)}`, "error");
    },
  });

  const trunkName = (id: string) =>
    trunks.data?.find((t) => t.id === id)?.name ?? id;

  const trunkColumns: GridColDef<SIPTrunk>[] = [
    { field: "name", headerName: "Name", flex: 0.6, minWidth: 140 },
    {
      field: "state",
      headerName: "State",
      flex: 0.3,
      minWidth: 100,
      renderCell: ({ row }) => <TrunkStateChip state={row.status.state} />,
    },
    {
      field: "enabled",
      headerName: "Enabled",
      flex: 0.3,
      minWidth: 90,
      valueGetter: (_value, row) => yesNo(row.enabled),
    },
    {
      field: "trusted",
      headerName: "Trusted",
      flex: 0.3,
      minWidth: 90,
      valueGetter: (_value, row) => yesNo(row.trusted),
    },
    {
      field: "addresses",
      headerName: "Addresses",
      flex: 1,
      minWidth: 200,
      renderCell: ({ row }) => (
        <Box component="ul" sx={{ m: 0, p: 0, listStyle: "none" }}>
          {row.addresses.map((a) => (
            <li key={formatAddress(a)}>{formatAddress(a)}</li>
          ))}
        </Box>
      ),
    },
    {
      field: "actions",
      headerName: "",
      width: 96,
      align: "right",
      sortable: false,
      renderCell: ({ row }) => (
        <ActionButtons
          name={row.name}
          onEdit={() => setTrunkDialog({ trunk: row })}
          onDelete={() => {
            trunkDeletion.reset();
            setDeletingTrunk(row);
          }}
        />
      ),
    },
  ];

  const routeColumns: GridColDef<SIPRoute>[] = [
    {
      field: "prefix",
      headerName: "Prefix",
      flex: 0.4,
      minWidth: 120,
      valueGetter: (_value, row) => routeName(row),
    },
    {
      field: "trunks",
      headerName: "Trunks",
      flex: 1,
      minWidth: 200,
      valueGetter: (_value, row) => row.trunks.map(trunkName).join(", "),
    },
    {
      field: "actions",
      headerName: "",
      width: 96,
      align: "right",
      sortable: false,
      renderCell: ({ row }) => (
        <ActionButtons
          name={`route ${routeName(row)}`}
          onEdit={() => setRouteDialog({ route: row })}
          onDelete={() => {
            routeDeletion.reset();
            setDeletingRoute(row);
          }}
        />
      ),
    },
  ];

  const visible = <T extends GridValidRowModel>(columns: GridColDef<T>[]) =>
    editable ? columns : columns.filter((c) => c.field !== "actions");

  const gridProps = {
    disableColumnSorting: true,
    disableColumnFilter: true,
    disableColumnMenu: true,
    disableRowSelectionOnClick: true,
    disableVirtualization: true,
    hideFooter: true,
    getRowHeight: () => "auto" as const,
    autoHeight: true,
    sx: gridSx,
  };

  return (
    <Box component="section" aria-labelledby="sip-trunks-title">
      <PageHeader id="sip-trunks-title" title="SIP Trunks" />

      <SectionHeader
        id="trunks-title"
        title={trunks.data ? `Trunks (${trunks.data.length})` : "Trunks"}
        first
      >
        {editable && (
          <Button
            variant="contained"
            color="success"
            size="small"
            onClick={() => setTrunkDialog({})}
          >
            Create Trunk
          </Button>
        )}
      </SectionHeader>
      <QueryAlert query={trunks} subject="SIP trunks" />
      {trunks.isPending && <Skeleton variant="rounded" height={160} />}
      {trunks.data && (
        <DataGrid<SIPTrunk>
          {...gridProps}
          aria-labelledby="trunks-title"
          rows={trunks.data}
          columns={visible(trunkColumns)}
          localeText={{ noRowsLabel: "No trunks." }}
        />
      )}

      <SectionHeader
        id="routes-title"
        title={routes.data ? `Routes (${routes.data.length})` : "Routes"}
      >
        {editable && (
          <Button
            variant="contained"
            color="success"
            size="small"
            disabled={!trunks.data?.length}
            onClick={() => setRouteDialog({})}
          >
            Create Route
          </Button>
        )}
      </SectionHeader>
      <QueryAlert query={routes} subject="SIP routes" />
      {routes.isPending && <Skeleton variant="rounded" height={120} />}
      {routes.data && (
        <DataGrid<SIPRoute>
          {...gridProps}
          aria-labelledby="routes-title"
          rows={routes.data.toSorted(byPrefix)}
          columns={visible(routeColumns)}
          localeText={{ noRowsLabel: "No routes." }}
        />
      )}

      {trunkDialog && (
        <SIPTrunkDialog
          trunk={trunkDialog.trunk}
          onClose={() => setTrunkDialog(null)}
        />
      )}
      {routeDialog && (
        <SIPRouteDialog
          route={routeDialog.route}
          trunks={trunks.data ?? []}
          onClose={() => setRouteDialog(null)}
        />
      )}
      {deletingTrunk && (
        <ConfirmDialog
          title="Delete Trunk"
          description={`Delete ${deletingTrunk.name}?`}
          confirmLabel="Delete"
          pending={trunkDeletion.isPending}
          onConfirm={() => trunkDeletion.mutate(deletingTrunk)}
          onClose={() => setDeletingTrunk(null)}
        />
      )}
      {deletingRoute && (
        <ConfirmDialog
          title="Delete Route"
          description={`Delete route ${routeName(deletingRoute)}?`}
          confirmLabel="Delete"
          pending={routeDeletion.isPending}
          onConfirm={() => routeDeletion.mutate(deletingRoute)}
          onClose={() => setDeletingRoute(null)}
        />
      )}
    </Box>
  );
}
