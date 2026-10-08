import { useState } from "react";
import { Box, Stack, TextField } from "@mui/material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import RegistrationDrawer from "@/components/RegistrationDrawer";
import SignallingPathChip from "@/components/SignallingPathChip";
import { useDebouncedValue } from "@/hooks/useDebouncedValue";
import { listRegistrations, type Registration } from "@/queries/registrations";
import { formatTimestamp } from "@/utils/dates";
import {
  imeiOf,
  lastExpiry,
  numbersOf,
  signallingPathOf,
} from "@/utils/registrations";

export const REGISTRATIONS_REFRESH_MS = 5000;

const SEARCH_DEBOUNCE_MS = 300;

const PAGE_SIZE_OPTIONS = [25, 50, 100];

const yesNo = (value: boolean) => (value ? "yes" : "no");

const lines = (values: string[]) =>
  values.length > 0 ? (
    <Box component="span" sx={{ display: "flex", flexDirection: "column" }}>
      {values.map((v) => (
        <span key={v}>{v}</span>
      ))}
    </Box>
  ) : (
    "—"
  );

const columns: GridColDef<Registration>[] = [
  {
    field: "impi",
    headerName: "Identity",
    flex: 1.2,
    minWidth: 200,
    renderCell: ({ row }) => <DomainName name={row.impi} />,
  },
  {
    field: "numbers",
    headerName: "Numbers",
    flex: 0.7,
    minWidth: 140,
    renderCell: ({ row }) => lines(numbersOf(row)),
  },
  {
    field: "imei",
    headerName: "IMEI",
    flex: 0.8,
    minWidth: 170,
    renderCell: ({ row }) =>
      lines([
        ...new Set(row.devices.map((d) => imeiOf(d) ?? d.instance ?? "—")),
      ]),
  },
  {
    field: "video",
    headerName: "Video",
    width: 80,
    valueGetter: (_value, row) =>
      yesNo(row.devices.some((d) => d.media.includes("video"))),
  },
  {
    field: "signalling",
    headerName: "Signalling Path",
    width: 160,
    renderCell: ({ row }) => (
      <SignallingPathChip path={signallingPathOf(row)} />
    ),
  },
  {
    field: "expires",
    headerName: "Expires",
    width: 170,
    valueGetter: (_value, row) => {
      const expiry = lastExpiry(row);
      return expiry ? formatTimestamp(expiry) : "—";
    },
  },
];

const gridSx = {
  bgcolor: "background.paper",
  "& .MuiDataGrid-row": { cursor: "pointer" },
  "& .MuiDataGrid-cell": {
    display: "flex",
    alignItems: "center",
    py: 1,
    overflowWrap: "anywhere",
  },
};

export default function Registrations() {
  const [search, setSearch] = useState("");
  const [pagination, setPagination] = useState<GridPaginationModel>({
    page: 0,
    pageSize: PAGE_SIZE_OPTIONS[0],
  });
  const [selected, setSelected] = useState<Registration | null>(null);

  const debouncedSearch = useDebouncedValue(search.trim(), SEARCH_DEBOUNCE_MS);

  const { data, error, isFetching } = useQuery({
    queryKey: ["registrations", debouncedSearch, pagination],
    queryFn: () =>
      listRegistrations({
        search: debouncedSearch,
        page: pagination.page + 1,
        perPage: pagination.pageSize,
      }),
    placeholderData: (prev) => prev,
    refetchInterval: REGISTRATIONS_REFRESH_MS,
  });

  // The drawer follows the refreshed list, and keeps the registration it shows once it leaves the list.
  const shown = selected
    ? (data?.items.find((r) => r.impi === selected.impi) ?? selected)
    : null;

  return (
    <Box component="section" aria-labelledby="registrations-title">
      <PageHeader
        id="registrations-title"
        title="Registrations"
        count={data?.total_count}
        description="Subscriptions registered with this IMS."
      />
      <Stack direction="row" sx={{ mb: 2 }}>
        <TextField
          label="Search"
          size="small"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            setPagination((p) => ({ ...p, page: 0 }));
          }}
          placeholder="IMSI or number"
          sx={{ minWidth: 260 }}
        />
      </Stack>
      <QueryAlert
        error={error}
        hasData={data !== undefined}
        subject="registrations"
      />
      <DataGrid<Registration>
        aria-labelledby="registrations-title"
        rows={data?.items ?? []}
        columns={columns}
        getRowId={(row) => row.impi}
        getRowHeight={() => "auto"}
        rowCount={data?.total_count ?? 0}
        loading={data === undefined && isFetching}
        paginationMode="server"
        paginationModel={pagination}
        onPaginationModelChange={setPagination}
        pageSizeOptions={PAGE_SIZE_OPTIONS}
        onRowClick={({ row }) => setSelected(row)}
        onCellKeyDown={({ row }, event) => {
          if (event.key === "Enter") setSelected(row);
        }}
        disableColumnSorting
        disableColumnFilter
        disableColumnMenu
        disableRowSelectionOnClick
        disableVirtualization
        autoHeight
        localeText={{ noRowsLabel: "No registrations." }}
        sx={gridSx}
      />
      <RegistrationDrawer
        registration={shown}
        onClose={() => setSelected(null)}
      />
    </Box>
  );
}
