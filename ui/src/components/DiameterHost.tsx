import { Chip, Stack, Tooltip, Typography } from "@mui/material";
import DomainName from "@/components/DomainName";
import { GENERATED_HOST_HINT } from "@/queries/cluster";

export default function DiameterHost({
  host,
  generated,
}: {
  host: string;
  generated: boolean;
}) {
  return (
    <Stack
      direction="row"
      spacing={1}
      sx={{ alignItems: "center", minWidth: 0 }}
    >
      <Typography
        variant="body2"
        component="span"
        title={host}
        sx={{ minWidth: 0 }}
      >
        <DomainName name={host} />
      </Typography>
      {generated && (
        <Tooltip title={GENERATED_HOST_HINT} arrow>
          <Chip
            label="Generated"
            size="small"
            variant="outlined"
            sx={{ flexShrink: 0 }}
          />
        </Tooltip>
      )}
    </Stack>
  );
}
