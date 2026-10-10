import { Chip, Tooltip, type ChipProps } from "@mui/material";
import type { DiameterPeerState } from "@/queries/diameter";

const stateColor = (state: DiameterPeerState): ChipProps["color"] => {
  switch (state) {
    case "open":
      return "success";
    case "down":
      return "error";
    default:
      return "warning";
  }
};

export default function PeerStateChip({
  state,
  error,
}: {
  state: DiameterPeerState;
  error?: string;
}) {
  const chip = <Chip label={state} color={stateColor(state)} size="small" />;

  return error && state !== "open" ? (
    <Tooltip title={error} arrow>
      {chip}
    </Tooltip>
  ) : (
    chip
  );
}
