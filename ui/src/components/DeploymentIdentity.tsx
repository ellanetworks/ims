import { Typography } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
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

  return (
    <Typography variant="body2" noWrap sx={{ opacity: 0.8 }}>
      {status.version}
    </Typography>
  );
}
