import { Box, Chip, Link, Tooltip, Typography } from "@mui/material";
import { OpenInNew as OpenInNewIcon } from "@mui/icons-material";
import CopyButton from "@/components/CopyButton";
import SettingsTable from "@/components/SettingsTable";
import {
  answersAsLeader,
  leaderOf,
  memberLabel,
  type ClusterStatus,
} from "@/queries/cluster";

function ToleranceChip({ failureTolerance }: { failureTolerance: number }) {
  if (failureTolerance < 1) {
    return (
      <Chip
        label="No failure tolerance"
        size="small"
        color="warning"
        variant="outlined"
      />
    );
  }

  const plural = failureTolerance === 1 ? "" : "s";

  return (
    <Chip
      label={`Tolerates ${failureTolerance} voter failure${plural}`}
      size="small"
      color="success"
      variant="outlined"
    />
  );
}

export function LeaderLink({ address }: { address: string }) {
  return (
    <Link
      href={address}
      target="_blank"
      rel="noreferrer"
      underline="hover"
      sx={{ display: "inline-flex", alignItems: "center" }}
    >
      {address}
      <OpenInNewIcon sx={{ fontSize: 16, ml: 0.5 }} />
    </Link>
  );
}

function HealthValue({ status }: { status: ClusterStatus }) {
  if (!status.leader_id) {
    return (
      <Tooltip title="No node holds leadership. The cluster cannot accept writes until enough voters recover for an election to succeed.">
        <Chip label="No leader" size="small" color="error" />
      </Tooltip>
    );
  }

  if (!answersAsLeader(status)) {
    const leader = leaderOf(status);
    return (
      <Typography variant="body2" sx={{ color: "text.secondary" }}>
        Shown on the leader
        {leader ? ` (node ${memberLabel(leader)})` : ""}
        {leader?.api_address ? (
          <>
            {": "}
            <LeaderLink address={leader.api_address} />
          </>
        ) : (
          "."
        )}
      </Typography>
    );
  }

  if (status.healthy === undefined) {
    return (
      <Tooltip title="The leader has not reported yet. This is normal for a moment after a leadership change.">
        <Chip label="Unknown" size="small" variant="outlined" />
      </Tooltip>
    );
  }

  return (
    <Box
      sx={{ display: "flex", alignItems: "center", gap: 1, flexWrap: "wrap" }}
    >
      <Chip
        label={status.healthy ? "Healthy" : "Unhealthy"}
        size="small"
        color={status.healthy ? "success" : "error"}
      />
      {status.failure_tolerance !== undefined && (
        <ToleranceChip failureTolerance={status.failure_tolerance} />
      )}
    </Box>
  );
}

export default function ClusterStateCard({
  status,
}: {
  status?: ClusterStatus;
}) {
  return (
    <SettingsTable
      label="Cluster state"
      loading={status === undefined}
      rows={[
        {
          label: "Cluster ID",
          help: "The identity shared by every member of this cluster.",
          value: status?.cluster_id ? (
            <Box
              sx={{
                display: "flex",
                alignItems: "center",
                gap: 0.5,
                minWidth: 0,
              }}
            >
              <Typography
                variant="body2"
                title={status.cluster_id}
                sx={{
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  fontFamily: "monospace",
                }}
              >
                {status.cluster_id}
              </Typography>
              <CopyButton value={status.cluster_id} label="Cluster ID" />
            </Box>
          ) : (
            "—"
          ),
        },
        {
          label: "Health",
          help: "Reported by the leader only: whether every voter is healthy, and how many voters can fail without stopping writes.",
          value: status && <HealthValue status={status} />,
        },
        {
          label: "Schema",
          help: "The database schema version of the cluster. It advances once every node runs the new version.",
          value: status && `v${status.schema}`,
        },
      ]}
    />
  );
}
