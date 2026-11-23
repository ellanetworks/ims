import type { ReactNode } from "react";
import {
  Box,
  IconButton,
  Skeleton,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableRow,
  Tooltip,
} from "@mui/material";
import { Edit as EditIcon } from "@mui/icons-material";
import CopyButton from "@/components/CopyButton";
import { TABLE_CONTAINER_SX } from "@/utils/layout";

export interface Copy {
  value: string;
  label: string;
}

export const LINE_SX = {
  minHeight: 30,
  display: "flex",
  alignItems: "center",
  gap: 1,
};

export interface SettingRow {
  label: string;
  help: string;
  value: ReactNode;
  onEdit?: () => void;
  copy?: Copy | Copy[];
}

export function SubFields({ rows }: { rows: [string, ReactNode][] }) {
  return (
    <Table size="small" sx={{ m: -1 }}>
      <TableBody>
        {rows.map(([label, value]) => (
          <TableRow
            key={label}
            sx={{ "& td": { borderBottom: "none", py: 0.5 } }}
          >
            <TableCell sx={{ fontWeight: 600, width: "40%", pl: 0 }}>
              {label}
            </TableCell>
            <TableCell sx={{ pl: 0 }}>{value}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

export default function SettingsTable({
  rows,
  loading,
  label,
}: {
  rows: SettingRow[];
  loading: boolean;
  label: string;
}) {
  return (
    <TableContainer sx={TABLE_CONTAINER_SX}>
      <Table aria-label={label}>
        <TableBody>
          {rows.map((row) => (
            <TableRow key={row.label}>
              <TableCell sx={{ fontWeight: 600, width: "35%" }}>
                <Tooltip title={row.help} arrow>
                  <span>{row.label}</span>
                </Tooltip>
              </TableCell>
              <TableCell sx={{ width: "55%" }}>
                {loading ? <Skeleton variant="text" width={160} /> : row.value}
              </TableCell>
              <TableCell sx={{ width: "10%" }}>
                <Stack
                  direction="row"
                  sx={{ justifyContent: "flex-end", gap: 0.5 }}
                >
                  {row.copy && !loading && (
                    <Stack sx={{ gap: 0.5 }}>
                      {[row.copy].flat().map((c) => (
                        <Box key={c.value} sx={LINE_SX}>
                          <CopyButton value={c.value} label={c.label} />
                        </Box>
                      ))}
                    </Stack>
                  )}
                  {row.onEdit && (
                    <IconButton
                      size="small"
                      onClick={row.onEdit}
                      disabled={loading}
                      aria-label={`Edit ${row.label}`}
                    >
                      <EditIcon fontSize="small" color="primary" />
                    </IconButton>
                  )}
                </Stack>
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </TableContainer>
  );
}
