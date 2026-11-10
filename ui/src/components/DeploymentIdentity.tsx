import { Typography } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import { nodeLabel } from "@/queries/cluster";
import { getStatus } from "@/queries/status";

export default function DeploymentIdentity() {
  const { data: status } = useQuery({
    queryKey: ["status"],
    queryFn: getStatus,
    staleTime: Infinity,
  });

  if (!status) {
    return null;
  }

  const label = status.node_id
    ? nodeLabel(status.display_name, status.diameter_label, status.node_id)
    : "";

  return (
    <Typography
      variant="body2"
      noWrap
      title={status.diameter_host || undefined}
      sx={{ opacity: 0.8 }}
    >
      {label ? `${status.version} · Node ${label}` : status.version}
    </Typography>
  );
}
