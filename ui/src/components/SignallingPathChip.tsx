import { Chip } from "@mui/material";
import type { SignallingPath } from "@/queries/registrations";

export default function SignallingPathChip({ path }: { path: SignallingPath }) {
  switch (path) {
    case "monitored":
      return <Chip label="monitored" color="success" size="small" />;
    case "lost":
      return <Chip label="lost" color="error" size="small" />;
    default:
      return <>—</>;
  }
}
