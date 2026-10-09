import { useState, type ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  IconButton,
  Skeleton,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  Edit as EditIcon,
} from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import CopyButton from "@/components/CopyButton";
import DiameterPeerDialog from "@/components/DiameterPeerDialog";
import DomainName from "@/components/DomainName";
import EditPolicyDialog from "@/components/EditPolicyDialog";
import EditRouteDialog from "@/components/EditRouteDialog";
import PageHeader from "@/components/PageHeader";
import PeerStateChip from "@/components/PeerStateChip";
import QueryAlert from "@/components/QueryAlert";
import SettingsTable, { type SettingRow } from "@/components/SettingsTable";
import {
  deleteDiameterPeer,
  type DiameterPeer,
  type DiameterRoute,
  getDiameterIdentity,
  listDiameterPeers,
  listDiameterRoutes,
} from "@/queries/diameter";
import { getPolicy, type PolicyWithStatus } from "@/queries/policy";
import { getSIPStatus } from "@/queries/sip";
import { formatEndpoint, hostOf, portOf } from "@/utils/addresses";
import { formatTimestamp } from "@/utils/dates";
import {
  applicationLabels,
  policyLabels,
  transportLabels,
} from "@/utils/labels";
import { changesTransports, RESTART_WARNING } from "@/utils/restart";

export const REFRESH_MS = 5000;

const gridSx = {
  width: "100%",
  border: 1,
  borderColor: "divider",
  bgcolor: "background.paper",
  "& .MuiDataGrid-cell": {
    borderBottom: "1px solid",
    borderColor: "divider",
    display: "flex",
    alignItems: "center",
    py: 1,
    overflowWrap: "anywhere",
  },
  "& .MuiDataGrid-columnHeaders": {
    borderBottom: "1px solid",
    borderColor: "divider",
  },
};

function Copyable({ value, label }: { value: string; label: string }) {
  return (
    <Stack direction="row" sx={{ alignItems: "center", gap: 0.5 }}>
      <span>
        <DomainName name={value} />
      </span>
      <CopyButton value={value} label={label} />
    </Stack>
  );
}

// SIP_PORT is where phones send their first REGISTER, since the core gives them only the P-CSCF's address
// (TS 24.229 §9.2.1, RFC 3261 §19.1.2).
const SIP_PORT = 5060;

const pcscfAddresses = (listeners: { role: string; address: string }[]) => {
  const seen = new Set<string>();
  return listeners
    .filter((l) => l.role === "pcscf")
    .map((l) => ({ ip: hostOf(l.address), port: portOf(l.address) }))
    .filter(({ ip }) => !seen.has(ip) && seen.add(ip));
};

const routeHelp: Record<DiameterRoute["application"], string> = {
  cx: "The realm of your HSS, where Cx requests go.",
  rx: "The realm of your PCRF, where Rx requests go.",
};

// routeState is the state of the best connection on a route: open if any of its peers is.
const routeState = (route: DiameterRoute | undefined) =>
  route?.peers.find((p) => p.status.state === "open")?.status.state ??
  route?.peers[0]?.status.state;

function policyStatus(
  policy: PolicyWithStatus,
  routes: DiameterRoute[] | undefined,
): ReactNode {
  if (policy.status.interface !== policy.interface) {
    return <Chip label="applying" color="info" size="small" />;
  }

  switch (policy.interface) {
    case "rx": {
      const state = routeState(routes?.find((r) => r.application === "rx"));
      return state ? <PeerStateChip state={state} /> : "—";
    }
    case "n5": {
      const last = policy.status.last;
      if (!last) return "—";
      return (
        <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
          <Chip
            label={last.reachable ? "reachable" : "unreachable"}
            color={last.reachable ? "success" : "error"}
            size="small"
          />
          <span>{last.result}</span>
          <span>{formatTimestamp(last.at)}</span>
        </Stack>
      );
    }
    default:
      return "—";
  }
}

export default function Cores() {
  const queryClient = useQueryClient();
  const [peerDialog, setPeerDialog] = useState<{ peer?: DiameterPeer } | null>(
    null,
  );
  const [deleting, setDeleting] = useState<DiameterPeer | null>(null);
  const [editingRoute, setEditingRoute] = useState<DiameterRoute | null>(null);
  const [editingPolicy, setEditingPolicy] = useState(false);

  const sip = useQuery({ queryKey: ["sip"], queryFn: getSIPStatus });
  const identity = useQuery({
    queryKey: ["diameter"],
    queryFn: getDiameterIdentity,
  });
  const peers = useQuery({
    queryKey: ["diameter-peers"],
    queryFn: listDiameterPeers,
    refetchInterval: REFRESH_MS,
  });
  const routes = useQuery({
    queryKey: ["diameter-routes"],
    queryFn: listDiameterRoutes,
    refetchInterval: REFRESH_MS,
  });
  const policy = useQuery({
    queryKey: ["policy"],
    queryFn: getPolicy,
    refetchInterval: REFRESH_MS,
  });

  const deletion = useMutation({
    mutationFn: (peer: DiameterPeer) => deleteDiameterPeer(peer.id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["diameter-peers"] });
      void queryClient.invalidateQueries({ queryKey: ["diameter-routes"] });
      void queryClient.invalidateQueries({ queryKey: ["policy"] });
      setDeleting(null);
    },
  });

  const columns: GridColDef<DiameterPeer>[] = [
    {
      field: "host",
      headerName: "Host",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) => <DomainName name={row.host} />,
    },
    {
      field: "realm",
      headerName: "Realm",
      flex: 1,
      minWidth: 180,
      renderCell: ({ row }) =>
        row.status.realm ? <DomainName name={row.status.realm} /> : "—",
    },
    {
      field: "address",
      headerName: "Address",
      flex: 0.7,
      minWidth: 150,
      valueGetter: (_value, row) =>
        formatEndpoint(row.address, row.port ?? 3868),
    },
    {
      field: "transport",
      headerName: "Transport",
      flex: 0.4,
      minWidth: 100,
      valueGetter: (_value, row) => transportLabels[row.transport ?? "tcp"],
    },
    {
      field: "applications",
      headerName: "Roles",
      flex: 0.5,
      minWidth: 110,
      valueGetter: (_value, row) =>
        row.applications.map((app) => applicationLabels[app]).join(", "),
    },
    {
      field: "priority",
      headerName: "Priority",
      flex: 0.3,
      minWidth: 90,
      valueGetter: (_value, row) => row.priority ?? 10,
    },
    {
      field: "state",
      headerName: "State",
      flex: 0.4,
      minWidth: 110,
      renderCell: ({ row }) => <PeerStateChip state={row.status.state} />,
    },
    {
      field: "since",
      headerName: "Since",
      flex: 0.6,
      minWidth: 170,
      valueGetter: (_value, row) =>
        row.status.since ? formatTimestamp(row.status.since) : "—",
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
            aria-label={`Edit ${row.host}`}
            onClick={() => setPeerDialog({ peer: row })}
          >
            <EditIcon fontSize="small" color="primary" />
          </IconButton>
          <IconButton
            size="small"
            aria-label={`Delete ${row.host}`}
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

  const policyRows: SettingRow[] = [
    {
      label: "Policy Function",
      help: "Where the IMS requests voice QoS for calls.",
      value: policy.data && policyLabels[policy.data.interface],
      onEdit: () => setEditingPolicy(true),
    },
  ];
  if (policy.data?.interface === "n5") {
    policyRows.push({
      label: "PCF URI",
      help: "The API root of the PCF.",
      value: policy.data.n5?.pcf_uri,
    });
  }
  if (policy.data?.status.notify) {
    policyRows.push({
      label: "Notification URI",
      help: "Where the PCF sends its notifications.",
      value: policy.data.status.notify,
    });
  }
  if (policy.data && policy.data.interface !== "none") {
    policyRows.push({
      label: "Status",
      help: "The policy function the IMS runs.",
      value: policyStatus(policy.data, routes.data),
    });
  }

  const routeRows: SettingRow[] = (routes.data ?? []).map((route) => ({
    label: `${applicationLabels[route.application]} Realm`,
    help: routeHelp[route.application],
    value: (
      <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
        <DomainName name={route.destination_realm} />
        {route.realm === "" && (
          <Chip label="home domain" size="small" variant="outlined" />
        )}
      </Stack>
    ),
    onEdit: () => setEditingRoute(route),
  }));

  return (
    <Box component="section" aria-labelledby="cores-title">
      <PageHeader
        id="cores-title"
        title="Cores"
        description="Diameter connections between this IMS and your core."
      />
      <QueryAlert
        error={sip.error ?? identity.error}
        hasData={sip.data !== undefined && identity.data !== undefined}
        subject="IMS identity"
      />
      <SettingsTable
        label="IMS identity"
        loading={sip.isPending || identity.isPending}
        rows={[
          {
            label: "P-CSCF Addresses",
            help: "Set them in your core's voice settings.",
            value:
              sip.data &&
              (pcscfAddresses(sip.data.listeners).length > 0 ? (
                <Box component="ul" sx={{ m: 0, p: 0, listStyle: "none" }}>
                  {pcscfAddresses(sip.data.listeners).map(({ ip, port }) => (
                    <Stack
                      component="li"
                      key={ip}
                      direction="row"
                      sx={{ alignItems: "center", gap: 1 }}
                    >
                      <Copyable value={ip} label={ip} />
                      {port !== SIP_PORT && (
                        <Tooltip
                          title={`Phones expect port ${SIP_PORT}.`}
                          arrow
                        >
                          <Chip
                            label={`port ${port}`}
                            color="warning"
                            size="small"
                          />
                        </Tooltip>
                      )}
                    </Stack>
                  ))}
                </Box>
              ) : (
                "—"
              )),
          },
          {
            label: "Diameter Host",
            help: "This IMS's Diameter name. Set by the Operator ID.",
            value: identity.data && (
              <Copyable value={identity.data.host} label="Diameter Host" />
            ),
          },
          {
            label: "Diameter Realm",
            help: "This IMS's Diameter realm. Set by the Operator ID.",
            value: identity.data && (
              <Copyable value={identity.data.realm} label="Diameter Realm" />
            ),
          },
        ]}
      />

      <Stack
        direction="row"
        sx={{
          alignItems: "center",
          justifyContent: "space-between",
          mt: 4,
          mb: 2,
        }}
      >
        <Typography id="peers-title" variant="h6" component="h2">
          {peers.data ? `Peers (${peers.data.length})` : "Peers"}
        </Typography>
        <Button
          variant="contained"
          color="success"
          size="small"
          startIcon={<AddIcon />}
          onClick={() => setPeerDialog({})}
        >
          Add Peer
        </Button>
      </Stack>
      <QueryAlert
        error={peers.error}
        hasData={peers.data !== undefined}
        subject="Diameter peers"
      />
      {peers.isPending && <Skeleton variant="rounded" height={160} />}
      {peers.data && (
        <DataGrid<DiameterPeer>
          aria-labelledby="peers-title"
          rows={peers.data}
          columns={columns}
          disableColumnSorting
          disableColumnFilter
          disableColumnMenu
          disableRowSelectionOnClick
          disableVirtualization
          hideFooter
          getRowHeight={() => "auto"}
          localeText={{ noRowsLabel: "No peers." }}
          autoHeight
          sx={gridSx}
        />
      )}

      <Typography
        id="routes-title"
        variant="h6"
        component="h2"
        sx={{ mt: 4, mb: 2 }}
      >
        Routes
      </Typography>
      <QueryAlert
        error={routes.error}
        hasData={routes.data !== undefined}
        subject="Diameter routes"
      />
      <SettingsTable
        label="Routes"
        loading={routes.isPending}
        rows={routeRows}
      />

      <Typography
        id="voice-qos-title"
        variant="h6"
        component="h2"
        sx={{ mt: 4, mb: 2 }}
      >
        Voice QoS
      </Typography>
      <QueryAlert
        error={policy.error}
        hasData={policy.data !== undefined}
        subject="voice QoS"
      />
      <SettingsTable
        label="Voice QoS"
        loading={policy.isPending}
        rows={policyRows}
      />

      {peerDialog && (
        <DiameterPeerDialog
          peer={peerDialog.peer}
          peers={peers.data ?? []}
          onClose={() => setPeerDialog(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={
            <>
              Delete <DomainName name={deleting.host} />?
            </>
          }
          action="Delete"
          pending={deletion.isPending}
          error={deletion.error}
          onConfirm={() => deletion.mutate(deleting)}
          onClose={() => setDeleting(null)}
        >
          {changesTransports(peers.data ?? [], deleting, undefined) && (
            <Alert severity="warning">{RESTART_WARNING}</Alert>
          )}
        </ConfirmDialog>
      )}
      {editingRoute && identity.data && (
        <EditRouteDialog
          route={editingRoute}
          homeDomain={identity.data.realm}
          onClose={() => setEditingRoute(null)}
        />
      )}
      {editingPolicy && policy.data && (
        <EditPolicyDialog
          policy={policy.data}
          onClose={() => setEditingPolicy(false)}
        />
      )}
    </Box>
  );
}
