import type { ReactNode } from "react";
import { Box, Typography } from "@mui/material";

// DrawerSection is a titled section of a drawer.
export default function DrawerSection({
  id,
  title,
  children,
}: {
  id: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <Box component="section" aria-labelledby={id}>
      <Typography id={id} variant="subtitle1" component="h3" sx={{ mb: 1 }}>
        {title}
      </Typography>
      {children}
    </Box>
  );
}
