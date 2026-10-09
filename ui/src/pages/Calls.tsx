import { useState } from "react";
import {
  Box,
  Button,
  Checkbox,
  FormControl,
  InputLabel,
  ListItemText,
  MenuItem,
  OutlinedInput,
  Select,
  Stack,
  TextField,
} from "@mui/material";
import { Edit as EditIcon } from "@mui/icons-material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import CallOutcomeChip from "@/components/CallOutcomeChip";
import CallRecordDrawer from "@/components/CallRecordDrawer";
import EditRetentionDialog from "@/components/EditRetentionDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import { useDebouncedValue } from "@/hooks/useDebouncedValue";
import {
  CALL_OUTCOMES,
  getCallRecord,
  getCallRecordRetention,
  listCallRecords,
  type CallOutcome,
  type CallRecord,
} from "@/queries/callRecords";
import {
  callerOf,
  calleeOf,
  formatDuration,
  outcomeLabels,
} from "@/utils/callRecords";
import { formatTimestamp } from "@/utils/dates";

export const CALLS_REFRESH_MS = 5000;

const SEARCH_DEBOUNCE_MS = 300;

const PAGE_SIZE_OPTIONS = [25, 50, 100];

const columns: GridColDef<CallRecord>[] = [
  {
    field: "requested_at",
    headerName: "Time",
    width: 170,
    valueGetter: (_value, row) => formatTimestamp(row.requested_at),
  },
  {
    field: "caller",
    headerName: "Caller",
    flex: 1,
    minWidth: 160,
    valueGetter: (_value, row) => callerOf(row),
  },
  {
    field: "callee",
    headerName: "Callee",
    flex: 1,
    minWidth: 160,
    valueGetter: (_value, row) => calleeOf(row),
  },
  {
    field: "outcome",
    headerName: "Outcome",
    width: 130,
    renderCell: ({ row }) => <CallOutcomeChip record={row} />,
  },
  {
    field: "duration",
    headerName: "Duration",
    width: 100,
    valueGetter: (_value, row) =>
      row.duration_ms !== undefined ? formatDuration(row.duration_ms) : "—",
  },
  {
    field: "media",
    headerName: "Media",
    width: 120,
    valueGetter: (_value, row) => row.media.join(", ") || "—",
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

const MINUTE_MS = 60_000;

// toRFC3339 is a local datetime-local value, plus offsetMs, as an RFC 3339 time, or nothing for an empty one.
const toRFC3339 = (local: string, offsetMs = 0): string | undefined => {
  if (local === "") return undefined;

  const d = new Date(local);
  return Number.isNaN(d.getTime())
    ? undefined
    : new Date(d.getTime() + offsetMs).toISOString();
};

export default function Calls() {
  const [search, setSearch] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [outcomes, setOutcomes] = useState<CallOutcome[]>([]);
  const [pagination, setPagination] = useState<GridPaginationModel>({
    page: 0,
    pageSize: PAGE_SIZE_OPTIONS[0],
  });
  const [selected, setSelected] = useState<CallRecord | null>(null);
  const [editing, setEditing] = useState(false);

  const debouncedSearch = useDebouncedValue(search.trim(), SEARCH_DEBOUNCE_MS);
  const firstPage = () => setPagination((p) => ({ ...p, page: 0 }));

  const filter = {
    search: debouncedSearch,
    from: toRFC3339(from),
    to: toRFC3339(to, MINUTE_MS),
    outcomes,
  };

  const { data, error, isFetching } = useQuery({
    queryKey: ["call-records", filter, pagination],
    queryFn: () =>
      listCallRecords({
        ...filter,
        page: pagination.page + 1,
        perPage: pagination.pageSize,
      }),
    placeholderData: (prev) => prev,
    refetchInterval: (query) =>
      pagination.page === 0 ||
      query.state.data?.items.some((r) => r.in_progress)
        ? CALLS_REFRESH_MS
        : false,
  });

  const retention = useQuery({
    queryKey: ["call-record-retention"],
    queryFn: getCallRecordRetention,
  });

  const record = useQuery({
    queryKey: ["call-record", selected?.id],
    queryFn: () => getCallRecord(selected?.id ?? 0),
    enabled: selected !== null,
    initialData: selected ?? undefined,
    refetchInterval: (query) =>
      query.state.data?.in_progress ? CALLS_REFRESH_MS : false,
  });

  const shown = selected ? (record.data ?? selected) : null;

  return (
    <Box component="section" aria-labelledby="calls-title">
      <PageHeader
        id="calls-title"
        title="Calls"
        count={data?.total_count}
        description="Calls made by the UEs registered with this IMS."
        action={
          retention.data && (
            <Button
              variant="outlined"
              startIcon={<EditIcon />}
              onClick={() => setEditing(true)}
            >
              Kept {retention.data.days} days
            </Button>
          )
        }
      />
      <Stack direction="row" sx={{ mb: 2, gap: 2, flexWrap: "wrap" }}>
        <TextField
          label="Search"
          size="small"
          value={search}
          onChange={(e) => {
            setSearch(e.target.value);
            firstPage();
          }}
          placeholder="Number, IMSI or ICID"
          sx={{ minWidth: 260 }}
        />
        <FormControl size="small" sx={{ minWidth: 200 }}>
          <InputLabel id="outcome-filter-label">Outcome</InputLabel>
          <Select<CallOutcome[]>
            labelId="outcome-filter-label"
            multiple
            value={outcomes}
            onChange={(e) => {
              const v = e.target.value;
              setOutcomes(
                (typeof v === "string" ? v.split(",") : v) as CallOutcome[],
              );
              firstPage();
            }}
            input={<OutlinedInput label="Outcome" />}
            renderValue={(chosen) =>
              chosen.map((o) => outcomeLabels[o]).join(", ")
            }
          >
            {CALL_OUTCOMES.map((o) => (
              <MenuItem key={o} value={o}>
                <Checkbox checked={outcomes.includes(o)} />
                <ListItemText primary={outcomeLabels[o]} />
              </MenuItem>
            ))}
          </Select>
        </FormControl>
        <TextField
          label="From"
          type="datetime-local"
          size="small"
          value={from}
          onChange={(e) => {
            setFrom(e.target.value);
            firstPage();
          }}
          slotProps={{ inputLabel: { shrink: true } }}
        />
        <TextField
          label="To"
          type="datetime-local"
          size="small"
          value={to}
          onChange={(e) => {
            setTo(e.target.value);
            firstPage();
          }}
          slotProps={{ inputLabel: { shrink: true } }}
        />
      </Stack>
      <QueryAlert error={error} hasData={data !== undefined} subject="calls" />
      <QueryAlert
        error={retention.error}
        hasData={retention.data !== undefined}
        subject="the call record retention"
      />
      <DataGrid<CallRecord>
        aria-labelledby="calls-title"
        rows={data?.items ?? []}
        columns={columns}
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
        localeText={{ noRowsLabel: "No calls." }}
        sx={gridSx}
      />
      <CallRecordDrawer record={shown} onClose={() => setSelected(null)} />
      {editing && retention.data && (
        <EditRetentionDialog
          retention={retention.data}
          onClose={() => setEditing(false)}
        />
      )}
    </Box>
  );
}
