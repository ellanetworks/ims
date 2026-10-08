import { useEffect, type ReactNode } from "react";
import { Box, Typography } from "@mui/material";

interface PageHeaderProps {
  id: string;
  title: string;
  count?: number;
  description?: string;
  action?: ReactNode;
}

export default function PageHeader({
  id,
  title,
  count,
  description,
  action,
}: PageHeaderProps) {
  useEffect(() => {
    document.title = `${title} · Ella IMS`;
  }, [title]);

  return (
    <Box sx={{ mb: 3 }}>
      <Box
        sx={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 2,
        }}
      >
        <Typography id={id} variant="h4" component="h1">
          {count === undefined ? title : `${title} (${count})`}
        </Typography>
        {action}
      </Box>
      {description && (
        <Typography variant="body1" color="textSecondary" sx={{ mt: 1 }}>
          {description}
        </Typography>
      )}
    </Box>
  );
}
