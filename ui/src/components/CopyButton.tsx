import { useState } from "react";
import { IconButton, Tooltip } from "@mui/material";
import {
  Check as CheckIcon,
  ContentCopy as ContentCopyIcon,
} from "@mui/icons-material";

export default function CopyButton({
  value,
  label,
}: {
  value: string;
  label: string;
}) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  };

  return (
    <Tooltip title={copied ? "Copied" : "Copy"} arrow>
      <IconButton size="small" onClick={copy} aria-label={`Copy ${label}`}>
        {copied ? (
          <CheckIcon fontSize="small" color="success" />
        ) : (
          <ContentCopyIcon fontSize="small" />
        )}
      </IconButton>
    </Tooltip>
  );
}
