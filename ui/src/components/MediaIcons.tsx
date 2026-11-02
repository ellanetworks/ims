import { Stack, Tooltip } from "@mui/material";
import {
  Call as CallIcon,
  Videocam as VideocamIcon,
} from "@mui/icons-material";

export default function MediaIcons({ media }: { media: string[] }) {
  return (
    <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
      {media.includes("audio") && (
        <Tooltip title="Voice" arrow>
          <CallIcon fontSize="small" color="action" aria-label="voice" />
        </Tooltip>
      )}
      {media.includes("video") && (
        <Tooltip title="Video" arrow>
          <VideocamIcon fontSize="small" color="action" aria-label="video" />
        </Tooltip>
      )}
    </Stack>
  );
}
