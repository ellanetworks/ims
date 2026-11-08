import type { ReactNode } from "react";
import { Box, Stack, Typography } from "@mui/material";

export default function SectionHeader({
  id,
  title,
  description,
  first,
  children,
}: {
  id: string;
  title: string;
  description: string;
  first?: boolean;
  children?: ReactNode;
}) {
  return (
    <Box sx={{ mt: first ? 0 : 4, mb: 2 }}>
      <Stack
        direction="row"
        sx={{ alignItems: "center", justifyContent: "space-between", gap: 2 }}
      >
        <Typography id={id} variant="h6" component="h2">
          {title}
        </Typography>
        {children}
      </Stack>
      <Typography variant="body2" color="text.secondary">
        {description}
      </Typography>
    </Box>
  );
}
