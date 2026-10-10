import { Stack, Tooltip } from "@mui/material";
import {
  Call as CallIcon,
  Videocam as VideocamIcon,
} from "@mui/icons-material";

// MediaIcons shows media as icons: a phone for voice, a camera for video.
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
