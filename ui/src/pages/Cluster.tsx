import { useMemo, useState, type ReactNode } from "react";
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Chip,
  Link,
  Skeleton,
  Stack,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  OpenInNew as OpenInNewIcon,
} from "@mui/icons-material";
import {
  DataGrid,
  GridActionsCellItem,
  type GridColDef,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import {
  AddNodeDialog,
  DrainNodeDialog,
  RemoveNodeDialog,
  RenameNodeDialog,
  ResumeNodeDialog,
} from "@/components/ClusterDialogs";
import ClusterStateCard from "@/components/ClusterStateCard";
import DiameterHost from "@/components/DiameterHost";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { useAuth } from "@/contexts/AuthContext";
import {
  answersAsLeader,
  getCluster,
  memberLabel,
  type ClusterMember,
  type DrainState,
} from "@/queries/cluster";
import { HA_DOCS_URL } from "@/utils/docs";
import { GRID_SX } from "@/utils/layout";

export const CLUSTER_REFRESH_MS = 5000;

type Action = "drain" | "resume" | "remove" | "rename";

function CenteredCell({ children }: { children: ReactNode }) {
  return (
    <Box
      sx={{
        display: "flex",
        alignItems: "center",
        height: "100%",
        width: "100%",
      }}
    >
      {children}
    </Box>
  );
}

function DrainStateChip({ state }: { state: DrainState }) {
  if (state === "draining") {
    return (
      <Chip label="Draining" size="small" color="warning" variant="outlined" />
    );
  }
  if (state === "drained") {
    return (
      <Chip label="Drained" size="small" color="error" variant="outlined" />
    );
  }
  return <Chip label="Active" size="small" variant="outlined" />;
}

function SuffrageChip({ member }: { member: ClusterMember }) {
  const voter = member.suffrage === "voter";
  return (
    <Chip
      label={
        member.suffrage === "nonvoter" ? "Nonvoter" : voter ? "Voter" : "None"
      }
      size="small"
      color={voter ? "primary" : "warning"}
      variant={voter ? "filled" : "outlined"}
    />
  );
}

function HealthChip({ member }: { member: ClusterMember }) {
  if (member.healthy === undefined) {
    return (
      <Tooltip title="The leader has not reported on this node yet." arrow>
        <Chip label="—" size="small" variant="outlined" />
      </Tooltip>
    );
  }
  return (
    <Chip
      label={member.healthy ? "Healthy" : "Unhealthy"}
      size="small"
      color={member.healthy ? "success" : "error"}
    />
  );
}

const columns = (
  selfId: string | undefined,
  versionsDiffer: boolean,
  showHealth: boolean,
  manageable: boolean,
  act: (action: Action, member: ClusterMember) => void,
): GridColDef<ClusterMember>[] => [
  {
    field: "display_name",
    headerName: "Node",
    flex: 1.5,
    minWidth: 180,
    renderCell: ({ row }) => (
      <Stack
        direction="row"
        spacing={1}
        sx={{ alignItems: "center", height: "100%", minWidth: 0 }}
      >
        <Typography
          variant="body2"
          noWrap
          title={row.node_id}
          sx={{ fontWeight: row.leader ? 700 : 400, minWidth: 0 }}
        >
          {memberLabel(row)}
        </Typography>
        {row.leader && (
          <Chip
            label="Leader"
            color="primary"
            size="small"
            sx={{ flexShrink: 0 }}
          />
        )}
        {row.node_id === selfId && (
          <Chip
            label="This node"
            size="small"
            variant="outlined"
            sx={{ flexShrink: 0 }}
          />
        )}
      </Stack>
    ),
  },
  {
    field: "address",
    headerName: "Cluster Address",
    flex: 1,
    minWidth: 140,
    valueGetter: (_value, row) => row.address || "—",
  },
  {
    field: "api_address",
    headerName: "API Address",
    flex: 1.2,
    minWidth: 180,
    renderCell: ({ row }) => (
      <CenteredCell>
        {row.api_address ? (
          <Link
            href={row.api_address}
            target="_blank"
            rel="noreferrer"
            underline="hover"
            noWrap
          >
            {row.api_address}
          </Link>
        ) : (
          <Typography variant="body2">—</Typography>
        )}
      </CenteredCell>
    ),
  },
  {
    field: "diameter_host",
    headerName: "Diameter Host",
    flex: 1.5,
    minWidth: 220,
    renderCell: ({ row }) => (
      <CenteredCell>
        <DiameterHost
          host={row.diameter_host}
          generated={row.label_generated}
        />
      </CenteredCell>
    ),
  },
  {
    field: "suffrage",
    headerName: "Suffrage",
    width: 100,
    renderCell: ({ row }) => (
      <CenteredCell>
        <SuffrageChip member={row} />
      </CenteredCell>
    ),
  },
  {
    field: "drain_state",
    headerName: "Drain",
    width: 100,
    renderCell: ({ row }) => (
      <CenteredCell>
        <DrainStateChip state={row.drain_state} />
      </CenteredCell>
    ),
  },
  {
    field: "version",
    headerName: "Version",
    width: 100,
    renderCell: ({ row }) => (
      <CenteredCell>
        {row.version ? (
          <Chip
            label={row.version}
            size="small"
            color={versionsDiffer ? "warning" : "default"}
            variant="outlined"
          />
        ) : (
          <Typography variant="body2">—</Typography>
        )}
      </CenteredCell>
    ),
  },
  ...(showHealth
    ? [
        {
          field: "healthy",
          headerName: "Healthy",
          width: 110,
          renderCell: ({ row }) => (
            <CenteredCell>
              <HealthChip member={row} />
            </CenteredCell>
          ),
        } satisfies GridColDef<ClusterMember>,
      ]
    : []),
  ...(manageable
    ? [
        {
          field: "actions",
          headerName: "Actions",
          type: "actions",
          width: 80,
          getActions: ({ row }) => [
            <GridActionsCellItem
              key="rename"
              showInMenu
              label="Rename this node"
              onClick={() => act("rename", row)}
            />,
            row.drain_state === "active" ? (
              <GridActionsCellItem
                key="drain"
                showInMenu
                label="Drain this node"
                onClick={() => act("drain", row)}
              />
            ) : (
              <GridActionsCellItem
                key="resume"
                showInMenu
                label="Resume this node"
                onClick={() => act("resume", row)}
              />
            ),
            <GridActionsCellItem
              key="remove"
              showInMenu
              label="Remove from cluster"
              onClick={() => act("remove", row)}
            />,
          ],
        } satisfies GridColDef<ClusterMember>,
      ]
    : []),
];

export default function Cluster() {
  const auth = useAuth();
  const [adding, setAdding] = useState(false);
  const [target, setTarget] = useState<{
    action: Action;
    member: ClusterMember;
  } | null>(null);

  const cluster = useQuery({
    queryKey: ["cluster"],
    queryFn: getCluster,
    refetchInterval: CLUSTER_REFRESH_MS,
  });
  const status = cluster.data;
  const members = useMemo(() => status?.members ?? [], [status]);
  const manageable = auth.can("cluster:manage") && status?.clustered === true;

  const close = () => setTarget(null);

  return (
    <Box component="section" aria-labelledby="cluster-title">
      <PageHeader
        id="cluster-title"
        title="Cluster"
        description="High-availability cluster members and health."
        action={
          manageable && (
            <Button
              variant="contained"
              color="success"
              startIcon={<AddIcon />}
              onClick={() => setAdding(true)}
              sx={{ flexShrink: 0 }}
            >
              Add Node
            </Button>
          )
        }
      />
      <QueryAlert query={cluster} subject="cluster status" />
      {cluster.isPending && <Skeleton variant="rounded" height={240} />}
      {status && !status.clustered && (
        <Alert severity="info">
          <AlertTitle>High availability is not enabled</AlertTitle>
          This node is running in standalone mode. Add a cluster block to its
          configuration file and restart it to enable clustering.{" "}
          <Link
            href={HA_DOCS_URL}
            target="_blank"
            rel="noreferrer"
            underline="hover"
            sx={{ display: "inline-flex", alignItems: "center" }}
          >
            Learn more
            <OpenInNewIcon sx={{ fontSize: 16, ml: 0.5 }} />
          </Link>
        </Alert>
      )}
      {status?.clustered && (
        <>
          <ClusterStateCard status={status} />
          <Typography
            id="cluster-nodes-title"
            variant="h6"
            component="h2"
            sx={{ mt: 4, mb: 2 }}
          >
            {`Nodes (${members.length})`}
          </Typography>
          <DataGrid<ClusterMember>
            aria-label="Nodes"
            rows={members}
            columns={columns(
              status.node_id,
              new Set(members.map((m) => m.version).filter(Boolean)).size > 1,
              answersAsLeader(status),
              manageable,
              (action, member) => setTarget({ action, member }),
            )}
            getRowId={(m) => m.node_id}
            disableColumnSorting
            disableColumnFilter
            disableColumnMenu
            disableRowSelectionOnClick
            disableVirtualization
            hideFooter
            localeText={{
              noRowsLabel: "No members.",
              actionsCellMore: "Node actions",
            }}
            autoHeight
            sx={GRID_SX}
          />
        </>
      )}
      {adding && <AddNodeDialog onClose={() => setAdding(false)} />}
      {target?.action === "rename" && (
        <RenameNodeDialog member={target.member} onClose={close} />
      )}
      {target?.action === "drain" && (
        <DrainNodeDialog
          member={target.member}
          isSelf={target.member.node_id === status?.node_id}
          isLastActive={
            !members.some(
              (m) =>
                m.node_id !== target.member.node_id &&
                m.drain_state === "active",
            )
          }
          onClose={close}
        />
      )}
      {target?.action === "resume" && (
        <ResumeNodeDialog member={target.member} onClose={close} />
      )}
      {target?.action === "remove" && (
        <RemoveNodeDialog
          member={target.member}
          isSelf={target.member.node_id === status?.node_id}
          onClose={close}
        />
      )}
    </Box>
  );
}
