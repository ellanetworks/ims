import type { ReactNode } from "react";
import { Box } from "@mui/material";

export default function Fields({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <Box
      component="dl"
      sx={{
        display: "grid",
        gridTemplateColumns: "max-content 1fr",
        columnGap: 2,
        rowGap: 0.5,
        m: 0,
        "& dt": { color: "text.secondary" },
        "& dd": { m: 0, overflowWrap: "anywhere" },
      }}
    >
      {rows.map(([label, value]) => (
        <Box key={label} sx={{ display: "contents" }}>
          <dt>{label}</dt>
          <dd>{value}</dd>
        </Box>
      ))}
    </Box>
  );
}
