import { Chip, type ChipProps } from "@mui/material";
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

export default function PeerStateChip({ state }: { state: DiameterPeerState }) {
  return <Chip label={state} color={stateColor(state)} size="small" />;
}
