import type { ReactNode } from "react";
import { Box } from "@mui/material";

export default function Lines<T>({
  items,
  itemKey,
  render,
}: {
  items: T[];
  itemKey: (item: T) => string;
  render: (item: T) => ReactNode;
}) {
  return (
    <Box
      component="ul"
      sx={{
        m: 0,
        p: 0,
        listStyle: "none",
        display: "grid",
        gridTemplateColumns: "minmax(0, 1fr)",
        gap: 0.5,
        width: "100%",
      }}
    >
      {items.map((item) => (
        <Box
          component="li"
          key={itemKey(item)}
          sx={{
            minHeight: 24,
            display: "flex",
            alignItems: "center",
            minWidth: 0,
          }}
        >
          {render(item)}
        </Box>
      ))}
    </Box>
  );
}
