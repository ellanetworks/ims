import { useState } from "react";
import {
  Alert,
  Box,
  Button,
  IconButton,
  Link,
  Skeleton,
  Stack,
} from "@mui/material";
import { Delete as DeleteIcon, Edit as EditIcon } from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link as RouterLink } from "react-router-dom";
import ConfirmDialog from "@/components/ConfirmDialog";
import Copyable from "@/components/Copyable";
import DomainName from "@/components/DomainName";
import EditIPSMGWDialog from "@/components/EditIPSMGWDialog";
import EditRouteDialog from "@/components/EditRouteDialog";
import PageHeader from "@/components/PageHeader";
import PeerStateChip from "@/components/PeerStateChip";
import QueryAlert from "@/components/QueryAlert";
import { realmRow, RouteStatus } from "@/components/RealmRow";
import SectionHeader from "@/components/SectionHeader";
import SettingsTable from "@/components/SettingsTable";
import SMSRouteDialog, { smscPeers } from "@/components/SMSRouteDialog";
import { useAuth } from "@/contexts/AuthContext";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import {
  type DiameterPeer,
  type DiameterRoute,
  getDiameterIdentity,
  listDiameterPeers,
  listDiameterRoutes,
  peerName,
} from "@/queries/diameter";
import {
  deleteSMSRoute,
  getIPSMGW,
  listSMSRoutes,
  type SMSRoute,
  smsRouteName,
} from "@/queries/ipsmgw";
import { GRID_SX } from "@/utils/layout";
import { REFRESH_MS } from "@/pages/Cores";

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

const byServiceCentre = (a: SMSRoute, b: SMSRoute) =>
  a.service_centre === "" || b.service_centre === ""
    ? Number(a.service_centre === "") - Number(b.service_centre === "")
    : a.service_centre.localeCompare(b.service_centre);

function RoutePeers({ ids, peers }: { ids: string[]; peers: DiameterPeer[] }) {
  const known = peers
    .filter((p) => ids.includes(p.id))
    .toSorted((a, b) => (a.priority ?? 0) - (b.priority ?? 0));
  return (
    <Box
      component="ul"
      sx={{ m: 0, p: 0, listStyle: "none", display: "grid", gap: 0.5 }}
    >
      {known.map((p) => (
        <Stack
          component="li"
          key={p.id}
          direction="row"
          sx={{ alignItems: "center", gap: 1 }}
        >
          <span>
            <DomainName name={peerName(p)} />
          </span>
          <PeerStateChip state={p.status.state} error={p.status.error} />
        </Stack>
      ))}
    </Box>
  );
}

export default function SMS() {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const auth = useAuth();
  const editable = auth.can("ip_sm_gw:update");
  const [editingNumber, setEditingNumber] = useState(false);
  const [editingRealm, setEditingRealm] = useState<DiameterRoute | null>(null);
  const [routeDialog, setRouteDialog] = useState<{ route?: SMSRoute } | null>(
    null,
  );
  const [deleting, setDeleting] = useState<SMSRoute | null>(null);

  const ipsmgw = useQuery({ queryKey: ["ip-sm-gw"], queryFn: getIPSMGW });
  const identity = useQuery({
    queryKey: ["diameter"],
    queryFn: getDiameterIdentity,
  });
  const routes = useQuery({
    queryKey: ["sms-routes"],
    queryFn: listSMSRoutes,
  });
  const peers = useQuery({
    queryKey: ["diameter-peers"],
    queryFn: listDiameterPeers,
    refetchInterval: REFRESH_MS,
  });
  const diameterRoutes = useQuery({
    queryKey: ["diameter-routes"],
    queryFn: listDiameterRoutes,
    refetchInterval: REFRESH_MS,
  });

  const deletion = useMutation({
    mutationFn: (route: SMSRoute) => deleteSMSRoute(route.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["sms-routes"] });
      setDeleting(null);
      showSnackbar("Route deleted successfully.", "success");
    },
    onError: (error) => {
      setDeleting(null);
      showSnackbar(`Failed to delete route: ${errorMessage(error)}`, "error");
    },
  });

  const s6c = diameterRoutes.data?.find((r) => r.application === "s6c");
  const eligible = smscPeers(peers.data ?? []);

  const allColumns: GridColDef<SMSRoute>[] = [
    {
      field: "service_centre",
      headerName: "Service Centre",
      flex: 0.4,
      minWidth: 140,
      valueGetter: (_value, row) => smsRouteName(row),
    },
    {
      field: "peers",
      headerName: "SMSC Peers",
      flex: 1,
      minWidth: 240,
      renderCell: ({ row }) => (
        <RoutePeers ids={row.peers} peers={peers.data ?? []} />
      ),
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
            aria-label={`Edit route ${smsRouteName(row)}`}
            onClick={() => setRouteDialog({ route: row })}
          >
            <EditIcon fontSize="small" color="primary" />
          </IconButton>
          <IconButton
            size="small"
            aria-label={`Delete route ${smsRouteName(row)}`}
            onClick={() => {
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

  return (
    <Box component="section" aria-labelledby="sms-title">
      <PageHeader
        id="sms-title"
        title="SMS over IP"
        description="How phones on IMS send and receive SMS through your SMSC."
      />

      <SectionHeader
        id="ipsmgw-title"
        title="IP-SM-GW"
        description="The built-in IP-SM-GW. Enter its name in your HSS, and its Diameter host and realm in your SMSC."
        first
      />
      <QueryAlert query={ipsmgw} subject="IP-SM-GW" />
      <QueryAlert query={identity} subject="IMS identity" />
      {ipsmgw.data?.number === "" && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          No IP-SM-GW number: phones get no SMS over IP.
        </Alert>
      )}
      <SettingsTable
        label="IP-SM-GW"
        loading={ipsmgw.isPending || identity.isPending}
        rows={[
          {
            label: "Name",
            help: "The application server of your iFCs for SMS over IP.",
            value: ipsmgw.data && (
              <Copyable value={ipsmgw.data.name} label="IP-SM-GW Name" />
            ),
          },
          {
            label: "Number",
            help: "The IP-SM-GW registers it at the HSS for phones that can take SMS over IP. Enter it in the HSS only if your HSS needs it preconfigured.",
            value: ipsmgw.data && (ipsmgw.data.number || "—"),
            onEdit:
              ipsmgw.data && editable
                ? () => setEditingNumber(true)
                : undefined,
          },
          {
            label: "Diameter Host",
            help: "This IMS's Diameter name, which the SMSC sends short messages to.",
            value: identity.data && (
              <Copyable value={identity.data.host} label="Diameter Host" />
            ),
          },
          {
            label: "Diameter Realm",
            help: "This IMS's Diameter realm.",
            value: identity.data && (
              <Copyable value={identity.data.realm} label="Diameter Realm" />
            ),
          },
        ]}
      />

      <SectionHeader
        id="sms-hss-title"
        title="HSS"
        description="Where the IP-SM-GW gets routing information, reports delivery status, and receives the routing requests the HSS forwards."
      >
        <RouteStatus route={s6c} />
      </SectionHeader>
      <QueryAlert query={diameterRoutes} subject="Diameter routes" />
      <SettingsTable
        label="HSS"
        loading={diameterRoutes.isPending}
        rows={[
          realmRow(
            s6c,
            "s6c",
            "The realm of your HSS, where S6c requests go.",
            auth.can("diameter:update") ? setEditingRealm : undefined,
          ),
        ]}
      />

      <SectionHeader
        id="sms-routes-title"
        title={routes.data ? `Routes (${routes.data.length})` : "Routes"}
        description="The SMS-IWMSC of your SMSC that each short message from a phone goes to, by the service centre address the phone gives."
      >
        {editable && (
          <Button
            variant="contained"
            color="success"
            size="small"
            disabled={eligible.length === 0}
            onClick={() => setRouteDialog({})}
          >
            Create Route
          </Button>
        )}
      </SectionHeader>
      <QueryAlert query={routes} subject="SMS routes" />
      <QueryAlert query={peers} subject="Diameter peers" />
      {peers.data && eligible.length === 0 && (
        <Alert severity="info" sx={{ mb: 2 }}>
          No SMSC peer. Create a peer with the SGd role and a host on the{" "}
          <Link component={RouterLink} to="/cores">
            Cores
          </Link>{" "}
          page.
        </Alert>
      )}
      {routes.data?.length === 0 && eligible.length > 0 && (
        <Alert severity="warning" sx={{ mb: 2 }}>
          No route: short messages from phones go nowhere.
        </Alert>
      )}
      {routes.isPending && <Skeleton variant="rounded" height={120} />}
      {routes.data && (
        <DataGrid<SMSRoute>
          aria-labelledby="sms-routes-title"
          rows={routes.data.toSorted(byServiceCentre)}
          columns={columns}
          disableColumnSorting
          disableColumnFilter
          disableColumnMenu
          disableRowSelectionOnClick
          disableVirtualization
          hideFooter
          getRowHeight={() => "auto"}
          localeText={{ noRowsLabel: "No routes." }}
          autoHeight
          sx={gridSx}
        />
      )}

      {editingNumber && ipsmgw.data && (
        <EditIPSMGWDialog
          ipsmgw={ipsmgw.data}
          onClose={() => setEditingNumber(false)}
        />
      )}
      {editingRealm && (
        <EditRouteDialog
          route={editingRealm}
          homeDomain={identity.data?.realm ?? ""}
          onClose={() => setEditingRealm(null)}
        />
      )}
      {routeDialog && (
        <SMSRouteDialog
          route={routeDialog.route}
          peers={peers.data ?? []}
          onClose={() => setRouteDialog(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete Route"
          description={`Delete route ${smsRouteName(deleting)}?`}
          confirmLabel="Delete"
          pending={deletion.isPending}
          onConfirm={() => deletion.mutate(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </Box>
  );
}
