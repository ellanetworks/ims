import type { ReactNode } from "react";
import {
  Alert,
  Box,
  Button,
  Chip,
  Divider,
  Drawer,
  IconButton,
  Paper,
  Stack,
  Typography,
} from "@mui/material";
import { Close as CloseIcon } from "@mui/icons-material";
import { useMutation } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import Fields from "@/components/Fields";
import SignallingPathChip from "@/components/SignallingPathChip";
import {
  reauthenticate,
  type RegisteredDevice,
  type Registration,
} from "@/queries/registrations";
import { formatTimestamp } from "@/utils/dates";
import { imeiOf } from "@/utils/registrations";

const yesNo = (value: boolean) => (value ? "yes" : "no");

function DeviceCard({ device }: { device: RegisteredDevice }) {
  const rows: [string, ReactNode][] = [
    ["IMEI", imeiOf(device) ?? device.instance ?? "—"],
    ["Address", device.address ?? "—"],
    ["Transport", device.transport?.toUpperCase() ?? "—"],
    // Without the P-CSCF's flow, whether IPsec protects the device is unknown.
    ["IPsec", device.address ? yesNo(device.protected) : "—"],
    ["Media", device.media.join(", ") || "—"],
    [
      "Signalling Path",
      <SignallingPathChip key="path" path={device.signalling_path} />,
    ],
    ["Registered", formatTimestamp(device.registered_at)],
    ["Expires", formatTimestamp(device.expires_at)],
    ["Contact", device.contact],
  ];

  return (
    <Paper component="li" variant="outlined" sx={{ p: 1.5 }}>
      <Fields rows={rows} />
    </Paper>
  );
}

function RegistrationDetail({
  registration,
  onClose,
}: {
  registration: Registration;
  onClose: () => void;
}) {
  const reauth = useMutation({
    mutationFn: () => reauthenticate(registration.impi),
  });

  return (
    <Stack spacing={2} sx={{ p: 2 }}>
      <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>
        <Typography
          id="registration-drawer-title"
          variant="h6"
          component="h2"
          sx={{ flexGrow: 1, overflowWrap: "anywhere" }}
        >
          <DomainName name={registration.impi} />
        </Typography>
        <IconButton aria-label="Close" onClick={onClose}>
          <CloseIcon />
        </IconButton>
      </Stack>
      <Stack direction="row" sx={{ alignItems: "center", gap: 1 }}>
        <Button
          variant="outlined"
          onClick={() => reauth.mutate()}
          disabled={reauth.isPending}
        >
          Re-authenticate
        </Button>
        {reauth.isSuccess && (
          <Chip label="requested" color="info" size="small" />
        )}
      </Stack>
      {reauth.error && (
        <Alert severity="error">
          Could not re-authenticate: {reauth.error.message}
        </Alert>
      )}
      <Divider />
      <Box component="section" aria-labelledby="identities-title">
        <Typography
          id="identities-title"
          variant="subtitle1"
          component="h3"
          sx={{ mb: 1 }}
        >
          Public Identities ({registration.identities.length})
        </Typography>
        <Stack
          component="ul"
          spacing={0.5}
          sx={{ m: 0, p: 0, listStyle: "none" }}
        >
          {registration.identities.map((identity) => (
            <Stack
              component="li"
              key={identity.uri}
              direction="row"
              sx={{ alignItems: "center", gap: 1, overflowWrap: "anywhere" }}
            >
              <span>{identity.uri}</span>
              {identity.display_name && (
                <Typography variant="body2" color="textSecondary">
                  {identity.display_name}
                </Typography>
              )}
              {identity.barred && <Chip label="barred" size="small" />}
            </Stack>
          ))}
        </Stack>
      </Box>
      <Divider />
      <Box component="section" aria-labelledby="contacts-title">
        <Typography
          id="contacts-title"
          variant="subtitle1"
          component="h3"
          sx={{ mb: 1 }}
        >
          Contacts ({registration.devices.length})
        </Typography>
        <Stack
          component="ul"
          spacing={1}
          sx={{ m: 0, p: 0, listStyle: "none" }}
        >
          {registration.devices.map((device) => (
            <DeviceCard key={device.contact} device={device} />
          ))}
        </Stack>
      </Box>
    </Stack>
  );
}

export default function RegistrationDrawer({
  registration,
  onClose,
}: {
  registration: Registration | null;
  onClose: () => void;
}) {
  return (
    <Drawer
      anchor="right"
      open={registration !== null}
      onClose={onClose}
      sx={{ zIndex: (theme) => theme.zIndex.modal }}
      slotProps={{
        paper: {
          sx: { width: { xs: "100%", sm: 520 } },
          "aria-labelledby": "registration-drawer-title",
        },
      }}
    >
      {registration && (
        <RegistrationDetail
          key={registration.impi}
          registration={registration}
          onClose={onClose}
        />
      )}
    </Drawer>
  );
}
