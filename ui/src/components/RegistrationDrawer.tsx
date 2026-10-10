import type { ReactNode } from "react";
import {
  Box,
  Divider,
  Drawer,
  IconButton,
  Link,
  Stack,
  Typography,
} from "@mui/material";
import { Close as CloseIcon } from "@mui/icons-material";
import DomainName from "@/components/DomainName";
import Fields from "@/components/Fields";
import type {
  RegisteredContact,
  Registration,
  SignallingPath,
} from "@/queries/registrations";
import { formatTimestamp } from "@/utils/dates";
import {
  contactsOf,
  devicesOf,
  imeiOf,
  numbersOf,
  othersOf,
  subscriberOf,
  type Device,
} from "@/utils/registrations";

const mediaLabels: Record<RegisteredContact["media"][number], string> = {
  audio: "Voice",
  video: "Video",
};

const signallingLabels: Record<SignallingPath, string> = {
  monitored: "Monitored",
  unmonitored: "Not monitored",
  lost: "Lost",
};

// contactRows are the fields of a contact of a device, or of one of its registration flows.
const contactRows = (contact: RegisteredContact): [string, ReactNode][] => [
  ["Address", contact.address ?? "—"],
  ["Transport", contact.transport?.toUpperCase() ?? "—"],
  // Without the P-CSCF's flow, whether IPsec protects the contact is unknown.
  ["IPsec", contact.address ? (contact.protected ? "Yes" : "No") : "—"],
  // RFC 3840 §9: the media the contact registered for, as it declared them.
  ["Media", contact.media.map((m) => mediaLabels[m]).join(", ") || "—"],
  [
    "Signalling",
    <Typography
      key="signalling"
      variant="inherit"
      component="span"
      color={contact.signalling_path === "lost" ? "error" : "inherit"}
    >
      {signallingLabels[contact.signalling_path]}
    </Typography>,
  ],
  ["Registered", formatTimestamp(contact.registered_at)],
  ["Expires", formatTimestamp(contact.expires_at)],
];

function Section({
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

// DeviceSection lists a device's fields, and those of each of its registration flows (RFC 5626) when it has
// several.
function DeviceSection({ device, index }: { device: Device; index: number }) {
  const [first] = device.contacts;
  const imei = imeiOf(first);
  const identity: [string, ReactNode][] = imei
    ? [["IMEI", imei]]
    : first.instance
      ? [["Instance", first.instance]]
      : [];
  const title = `Device ${index + 1}`;

  if (device.contacts.length === 1) {
    return (
      <Section id={`device-title-${index}`} title={title}>
        <Fields rows={[...identity, ...contactRows(first)]} />
      </Section>
    );
  }

  return (
    <Section id={`device-title-${index}`} title={title}>
      <Stack spacing={1.5}>
        {identity.length > 0 && <Fields rows={identity} />}
        {device.contacts.map((contact, i) => {
          const flow = `Flow ${contact.reg_id ?? i + 1}`;
          return (
            <Box
              key={`${contact.contact}|${contact.reg_id ?? ""}`}
              component="section"
              aria-label={`${title} ${flow}`}
            >
              <Typography
                variant="body2"
                component="h4"
                sx={{ fontWeight: "medium", mb: 0.5 }}
              >
                {flow}
              </Typography>
              <Fields rows={contactRows(contact)} />
            </Box>
          );
        })}
      </Stack>
    </Section>
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
  const numbers = numbersOf(registration);
  const others = othersOf(registration);
  const subscriber = subscriberOf(registration.impi);
  const hss = [
    ...new Set(
      registration.implicit_registration_sets.flatMap((s) =>
        s.hss ? [s.hss.host] : [],
      ),
    ),
  ];
  const devices = devicesOf(contactsOf(registration));

  const rows: [string, ReactNode][] = [
    ["Number", numbers.length > 0 ? numbers.join(", ") : "—"],
  ];
  if (others.length > 0) {
    rows.push([
      "Also on",
      <Stack key="others" component="span" spacing={0.5}>
        {others.map((impi) => {
          const other = subscriberOf(impi);
          return (
            <Link
              key={impi}
              component="button"
              variant="inherit"
              onClick={() => onSearch(other)}
              sx={{ textAlign: "left", overflowWrap: "anywhere" }}
            >
              {other === impi ? <DomainName name={impi} /> : `IMSI ${other}`}
            </Link>
          );
        })}
      </Stack>,
    ]);
  }
  rows.push(
    [subscriber === registration.impi ? "IMPI" : "IMSI", subscriber],
    [
      "HSS",
      hss.length > 0
        ? hss.map((host) => (
            <div key={host}>
              <DomainName name={host} />
            </div>
          ))
        : "—",
    ],
  );

  return (
    // One key column width for all sections, so that their values line up.
    <Stack
      spacing={3}
      sx={{ p: 3, "& dl": { gridTemplateColumns: "7.5rem 1fr" } }}
    >
      <Stack direction="row" sx={{ alignItems: "flex-start", gap: 1 }}>
        <Typography
          id="registration-drawer-title"
          variant="h5"
          component="h2"
          sx={{ flexGrow: 1, overflowWrap: "anywhere" }}
        >
          {numbers[0] ?? <DomainName name={subscriber} />}
        </Typography>
        <IconButton aria-label="Close" onClick={onClose}>
          <CloseIcon />
        </IconButton>
      </Stack>
      <Divider />
      <Section id="subscriber-title" title="Subscriber">
        <Fields rows={rows} />
      </Section>
      {devices.map((device, i) => (
        <DeviceSection key={device.id} device={device} index={i} />
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
