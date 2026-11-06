import { Chip, type ChipProps } from "@mui/material";
import type { SIPTrunkState } from "@/queries/sipTrunks";

const stateColor: Record<SIPTrunkState, ChipProps["color"]> = {
  up: "success",
  down: "error",
  unknown: "default",
  unusable: "warning",
};

export default function TrunkStateChip({ state }: { state: SIPTrunkState }) {
  return <Chip label={state} color={stateColor[state]} size="small" />;
}
