import { useMemo, useState } from "react";
import {
  Box,
  IconButton,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from "@mui/material";
import {
  LockReset as LockResetIcon,
  Call as CallIcon,
  Videocam as VideocamIcon,
} from "@mui/icons-material";
import {
  DataGrid,
  type GridColDef,
  type GridPaginationModel,
} from "@mui/x-data-grid";
import { useMutation, useQuery } from "@tanstack/react-query";
import ConfirmDialog from "@/components/ConfirmDialog";
import DomainName from "@/components/DomainName";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import RegistrationDrawer from "@/components/RegistrationDrawer";
import SignallingPathChip from "@/components/SignallingPathChip";
import { useDebouncedValue } from "@/hooks/useDebouncedValue";
import {
  listRegistrations,
  reauthenticate,
  type Registration,
} from "@/queries/registrations";
import {
  contactsOf,
  devicesOf,
  numbersOf,
  signallingPathOf,
  subscriberOf,
} from "@/utils/registrations";

export const REGISTRATIONS_REFRESH_MS = 5000;

const SEARCH_DEBOUNCE_MS = 300;

const PAGE_SIZE_OPTIONS = [25, 50, 100];

function NumberCell({ registration }: { registration: Registration }) {
  const [first, ...more] = numbersOf(registration);

  return (
    <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
      <span>{first ?? "—"}</span>
      {more.length > 0 && (
        <Typography variant="body2" color="text.secondary">
          +{more.length}
        </Typography>
      )}
    </Stack>
  );
}

function DevicesCell({ registration }: { registration: Registration }) {
  const contacts = contactsOf(registration);
  const devices = devicesOf(contacts).length;
  // RFC 3840 §9: the media its devices registered for.
  const audio = contacts.some((c) => c.media.includes("audio"));
  const video = contacts.some((c) => c.media.includes("video"));

  return (
    <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
      <span>{devices}</span>
      {audio && (
        <Tooltip title="Voice capable" arrow>
          <CallIcon
            fontSize="small"
            color="action"
            aria-label="voice capable"
          />
        </Tooltip>
      )}
      {video && (
        <Tooltip title="Video capable" arrow>
          <VideocamIcon
            fontSize="small"
            color="action"
            aria-label="video capable"
          />
        </Tooltip>
      )}
      {signallingPathOf(registration) === "lost" && (
        <SignallingPathChip path="lost" />
      )}
    </Stack>
  );
}

const columnsFor = (
  onReauthenticate: (registration: Registration) => void,
): GridColDef<Registration>[] => [
  {
    field: "number",
    headerName: "Number",
    flex: 1,
    minWidth: 200,
    renderCell: ({ row }) => <NumberCell registration={row} />,
  },
  {
    field: "impi",
    headerName: "IMSI",
    flex: 1,
    minWidth: 180,
    renderCell: ({ row }) => <DomainName name={subscriberOf(row.impi)} />,
  },
  {
    field: "devices",
    headerName: "Devices",
    flex: 1,
    minWidth: 140,
    renderCell: ({ row }) => <DevicesCell registration={row} />,
  },
  {
    field: "actions",
    headerName: "Actions",
    width: 100,
    align: "right",
    headerAlign: "right",
    sortable: false,
    renderCell: ({ row }) => (
      <Tooltip title="Re-authenticate" arrow>
        <IconButton
          size="small"
          aria-label={`Re-authenticate ${subscriberOf(row.impi)}`}
          onClick={(e) => {
            e.stopPropagation();
            onReauthenticate(row);
          }}
        >
          <LockResetIcon fontSize="small" color="primary" />
        </IconButton>
      </Tooltip>
    ),
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
  const [reauthing, setReauthing] = useState<Registration | null>(null);

  const reauth = useMutation({
    mutationFn: (r: Registration) => reauthenticate(r.impi),
    onSuccess: () => setReauthing(null),
  });
  const columns = useMemo(() => columnsFor(setReauthing), []);

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
      {reauthing && (
        <ConfirmDialog
          title={`Re-authenticate ${numbersOf(reauthing)[0] ?? subscriberOf(reauthing.impi)}?`}
          action="Re-authenticate"
          pending={reauth.isPending}
          error={reauth.error}
          onConfirm={() => reauth.mutate(reauthing)}
          onClose={() => {
            setReauthing(null);
            reauth.reset();
          }}
        >
          <Typography>
            Its phones are asked to register again soon, authenticating anew
            with the HSS.
          </Typography>
        </ConfirmDialog>
      )}
      <RegistrationDrawer
        registration={shown}
        onClose={() => setSelected(null)}
        onSearch={(value) => {
          setSearch(value);
          setPagination((p) => ({ ...p, page: 0 }));
          setSelected(null);
        }}
      />
    </Box>
  );
}
