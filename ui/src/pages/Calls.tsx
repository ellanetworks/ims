import { useMemo, useState } from "react";
import {
  Box,
  Checkbox,
  FormControl,
  IconButton,
  InputLabel,
  ListItemText,
  MenuItem,
  OutlinedInput,
  Select,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import { Edit as EditIcon } from "@mui/icons-material";
import { DataGrid, type GridColDef } from "@mui/x-data-grid";
import { useQuery } from "@tanstack/react-query";
import CallOutcomeChip from "@/components/CallOutcomeChip";
import CallRecordDrawer from "@/components/CallRecordDrawer";
import EditRetentionDialog from "@/components/EditRetentionDialog";
import MediaIcons from "@/components/MediaIcons";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import TimeRangePicker, {
  RELATIVE_RANGES,
  timeRangeFilter,
  timeRangeParams,
  type RelativeRange,
} from "@/components/TimeRangePicker";
import { useDebouncedValue } from "@/hooks/useDebouncedValue";
import { useFilteredPagination } from "@/hooks/useFilteredPagination";
import { useSearchParamState } from "@/hooks/useSearchParamState";
import { useTimeRangeSearchParams } from "@/hooks/useTimeRangeSearchParams";
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
import { formatRecentTimestamp, formatTimestamp } from "@/utils/dates";

export const CALLS_REFRESH_MS = 5000;

const SEARCH_DEBOUNCE_MS = 300;

const PAGE_SIZE_OPTIONS = [25, 50, 100];

const DAY_MS = 24 * 60 * 60_000;

export const CALL_RANGES: RelativeRange[] = [
  ...RELATIVE_RANGES.filter((r) => r.value !== "5m"),
  { value: "30d", label: "Last 30 days", ms: 30 * DAY_MS },
  { value: "90d", label: "Last 90 days", ms: 90 * DAY_MS },
];

const DEFAULT_RANGE = "24h";

const outcomesOf = (param: string): CallOutcome[] =>
  param
    .split(",")
    .filter((o): o is CallOutcome => CALL_OUTCOMES.includes(o as CallOutcome));

const columns: GridColDef<CallRecord>[] = [
  {
    field: "requested_at",
    headerName: "Time",
    flex: 1,
    minWidth: 170,
    renderCell: ({ row }) => (
      <Tooltip title={formatTimestamp(row.requested_at)} arrow>
        <span>{formatRecentTimestamp(row.requested_at)}</span>
      </Tooltip>
    ),
  },
  {
    field: "caller",
    headerName: "Caller",
    flex: 1,
    minWidth: 150,
    valueGetter: (_value, row) => callerOf(row),
  },
  {
    field: "callee",
    headerName: "Callee",
    flex: 1,
    minWidth: 150,
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
    renderCell: ({ row }) =>
      row.media.length > 0 ? <MediaIcons media={row.media} /> : "—",
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

export default function Calls() {
  const [search, setSearch] = useSearchParamState("search");
  const [outcomeParam, setOutcomeParam] = useSearchParamState("outcome");
  const [timeRange, setTimeRange] = useTimeRangeSearchParams({
    defaultPreset: DEFAULT_RANGE,
  });
  const [selected, setSelected] = useState<CallRecord | null>(null);
  const [editing, setEditing] = useState(false);

  const debouncedSearch = useDebouncedValue(search.trim(), SEARCH_DEBOUNCE_MS);
  const outcomes = useMemo(() => outcomesOf(outcomeParam), [outcomeParam]);
  const timeFilter = useMemo(() => timeRangeFilter(timeRange), [timeRange]);

  const filter = { search: debouncedSearch, outcomes, ...timeFilter };
  const [pagination, setPagination] = useFilteredPagination(
    filter,
    PAGE_SIZE_OPTIONS[0],
  );

  const { data, error, isFetching } = useQuery({
    queryKey: ["call-records", filter, pagination],
    queryFn: () =>
      listCallRecords({
        search: debouncedSearch,
        outcomes,
        ...timeRangeParams(timeFilter, { ranges: CALL_RANGES }),
        page: pagination.page + 1,
        perPage: pagination.pageSize,
      }),
    placeholderData: (prev) => prev,
    refetchInterval: (query) =>
      debouncedSearch === "" &&
      (pagination.page === 0 ||
        query.state.data?.items.some((r) => r.in_progress))
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
        description="Calls to and from phones registered with this IMS."
      />
      <Stack
        direction={{ xs: "column", sm: "row" }}
        sx={{
          mb: 2,
          gap: 2,
          flexWrap: "wrap",
          alignItems: { xs: "flex-start", sm: "center" },
        }}
      >
        <TimeRangePicker
          value={timeRange}
          onChange={setTimeRange}
          ranges={CALL_RANGES}
        />
        <TextField
          label="Search"
          size="small"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
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
              setOutcomeParam(typeof v === "string" ? v : v.join(","));
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
        <Box
          sx={{
            display: "flex",
            alignItems: "center",
            gap: 1,
            ml: { sm: "auto" },
          }}
        >
          <Typography variant="body2" color="textSecondary">
            Retention: <strong>{retention.data?.days ?? "…"}</strong> days
          </Typography>
          {retention.data && (
            <IconButton
              aria-label="edit call record retention"
              size="small"
              color="primary"
              onClick={() => setEditing(true)}
            >
              <EditIcon fontSize="small" />
            </IconButton>
          )}
        </Box>
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
