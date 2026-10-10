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
  Tooltip,
  Typography,
} from "@mui/material";
import { Close as CloseIcon } from "@mui/icons-material";
import { useMutation } from "@tanstack/react-query";
import DomainName from "@/components/DomainName";
import Fields from "@/components/Fields";
import SignallingPathChip from "@/components/SignallingPathChip";
import {
  reauthenticate,
  type ImplicitRegistrationSet,
  type RegisteredContact,
  type RegisteredIdentity,
  type Registration,
} from "@/queries/registrations";
import { formatTimestamp } from "@/utils/dates";
import {
  devicesOf,
  numberOf,
  priorityOf,
  type Device,
} from "@/utils/registrations";

const yesNo = (value: boolean) => (value ? "yes" : "no");

function ContactCard({ contact }: { contact: RegisteredContact }) {
  const rows: [string, ReactNode][] = [
    ...(contact.reg_id !== undefined
      ? [["Flow", String(contact.reg_id)] as [string, ReactNode]]
      : []),
    ["Address", contact.address ?? "—"],
    ["Transport", contact.transport?.toUpperCase() ?? "—"],
    // Without the P-CSCF's flow, whether IPsec protects the contact is unknown.
    ["IPsec", contact.address ? yesNo(contact.protected) : "—"],
    ["Media", contact.media.join(", ") || "—"],
    ["Priority", priorityOf(contact)],
    [
      "Signalling Path",
      <SignallingPathChip key="path" path={contact.signalling_path} />,
    ],
    ["Registered", formatTimestamp(contact.registered_at)],
    ["Expires", formatTimestamp(contact.expires_at)],
    ["Contact", contact.contact],
  ];

  return (
    <Paper component="li" variant="outlined" sx={{ p: 1.5 }}>
      <Fields rows={rows} />
    </Paper>
  );
}

// DeviceSection lists the contacts of one device: its registration flows, or its contact.
function DeviceSection({ device }: { device: Device }) {
  return (
    <Box component="li">
      <Typography
        variant="body2"
        sx={{ fontWeight: "medium", mb: 0.5, overflowWrap: "anywhere" }}
      >
        {device.label}
      </Typography>
      <Stack
        component="ul"
        aria-label={device.label}
        spacing={1}
        sx={{ m: 0, p: 0, listStyle: "none" }}
      >
        {device.contacts.map((contact) => (
          <ContactCard
            key={`${contact.contact}|${contact.reg_id ?? ""}`}
            contact={contact}
          />
        ))}
      </Stack>
    </Box>
  );
}

// SharedChip marks a public identity other private identities are registered with: a request to it reaches their
// contacts too. It searches the identity, which lists them all.
function SharedChip({
  identity,
  onSearch,
}: {
  identity: RegisteredIdentity;
  onSearch: (search: string) => void;
}) {
  const n = identity.registered_with.length;
  if (n === 0) return null;

  const search = numberOf(identity) ?? identity.uri;

  return (
    <Tooltip title={identity.registered_with.join(", ")}>
      <Chip
        label={`shared with ${n} other${n === 1 ? "" : "s"}`}
        size="small"
        color="info"
        aria-label={`Search ${search}: also registered with ${identity.registered_with.join(", ")}`}
        onClick={() => onSearch(search)}
      />
    </Tooltip>
  );
}

// SetSection shows an implicit registration set: its HSS, its public identities and the devices bound to it. A
// registration with several sets numbers them.
function SetSection({
  set,
  index,
  count,
  onSearch,
}: {
  set: ImplicitRegistrationSet;
  index: number;
  count: number;
  onSearch: (search: string) => void;
}) {
  const devices = devicesOf(set.contacts);
  const id = (name: string) => `${name}-title-${index}`;
  const numbered = count > 1;
  const heading = numbered ? "h4" : "h3";

  return (
    <Stack
      component="section"
      spacing={2}
      aria-labelledby={numbered ? id("set") : undefined}
    >
      <Divider />
      {numbered && (
        <Typography id={id("set")} variant="h6" component="h3">
          Implicit Registration Set {index + 1}
        </Typography>
      )}
      <Fields
        rows={[
          [
            "HSS",
            set.hss ? (
              <>
                <DomainName name={set.hss.host} />
                <Typography variant="body2" color="textSecondary">
                  <DomainName name={set.hss.realm} />
                </Typography>
              </>
            ) : (
              "—"
            ),
          ],
        ]}
      />
      <Box component="section" aria-labelledby={id("identities")}>
        <Typography
          id={id("identities")}
          variant="subtitle1"
          component={heading}
          sx={{ mb: 1 }}
        >
          Public Identities ({set.identities.length})
        </Typography>
        <Stack
          component="ul"
          spacing={0.5}
          sx={{ m: 0, p: 0, listStyle: "none" }}
        >
          {set.identities.map((identity) => (
            <Stack
              component="li"
              key={identity.uri}
              direction="row"
              sx={{
                alignItems: "center",
                flexWrap: "wrap",
                columnGap: 1,
                rowGap: 0.5,
                overflowWrap: "anywhere",
              }}
            >
              <span>{identity.uri}</span>
              {identity.display_name && (
                <Typography variant="body2" color="textSecondary">
                  {identity.display_name}
                </Typography>
              )}
              {identity.barred && <Chip label="barred" size="small" />}
              <SharedChip identity={identity} onSearch={onSearch} />
            </Stack>
          ))}
        </Stack>
      </Box>
      <Box component="section" aria-labelledby={id("devices")}>
        <Typography
          id={id("devices")}
          variant="subtitle1"
          component={heading}
          sx={{ mb: 1 }}
        >
          Devices ({devices.length})
        </Typography>
        <Stack
          component="ul"
          spacing={2}
          sx={{ m: 0, p: 0, listStyle: "none" }}
        >
          {devices.map((device) => (
            <DeviceSection key={device.id} device={device} />
          ))}
        </Stack>
      </Box>
    </Stack>
  );
}

function RegistrationDetail({
  registration,
  onClose,
  onSearch,
}: {
  registration: Registration;
  onClose: () => void;
  onSearch: (search: string) => void;
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
      {registration.implicit_registration_sets.map((set, i) => (
        <SetSection
          key={i}
          set={set}
          index={i}
          count={registration.implicit_registration_sets.length}
          onSearch={onSearch}
        />
      ))}
    </Stack>
  );
}

export default function RegistrationDrawer({
  registration,
  onClose,
  onSearch,
}: {
  registration: Registration | null;
  onClose: () => void;
  onSearch: (search: string) => void;
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
          onSearch={onSearch}
        />
      )}
    </Drawer>
  );
}
