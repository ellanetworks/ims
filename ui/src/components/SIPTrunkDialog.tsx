import { useState } from "react";
import {
  Accordion,
  AccordionDetails,
  AccordionSummary,
  Box,
  Button,
  Checkbox,
  FormControlLabel,
  FormGroup,
  FormLabel,
  IconButton,
  MenuItem,
  Stack,
  TextField,
  Typography,
} from "@mui/material";
import {
  Add as AddIcon,
  Delete as DeleteIcon,
  ExpandMore as ExpandMoreIcon,
} from "@mui/icons-material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import Fields from "@/components/Fields";
import { useSnackbar } from "@/contexts/SnackbarContext";
import {
  type CertificateSummary,
  createSIPTrunk,
  type SIPTransport,
  type SIPTrunk,
  type SIPTrunkParams,
  type SIPTrunkTLSParams,
  updateSIPTrunk,
} from "@/queries/sipTrunks";
import { formatTimestamp } from "@/utils/dates";
import { sipTransportLabels } from "@/utils/labels";
import { trunkDefaults } from "@/utils/sipTrunks";

export const DEFAULT_KEEPALIVE_INTERVAL = 30;

const TRANSPORTS: SIPTransport[] = ["udp", "tcp", "tls"];

interface AddressRow {
  key: number;
  host: string;
  port: string;
  transport: SIPTransport | "";
}

let nextKey = 0;

const addressRow = (
  host = "",
  port = "",
  transport: SIPTransport | "" = "",
): AddressRow => ({ key: nextKey++, host, port, transport });

const splitList = (text: string): string[] =>
  text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "");

const PEM = /-----BEGIN CERTIFICATE-----[\s\S]*?-----END CERTIFICATE-----/g;

const splitPins = (text: string): string[] => [
  ...(text.match(PEM) ?? []),
  ...splitList(text.replace(PEM, " ")),
];

const validPort = (port: string) =>
  port === "" ||
  (/^\d+$/.test(port) && Number(port) >= 1 && Number(port) <= 65535);

const validKeepalive = (value: string) =>
  /^\d+$/.test(value) &&
  (Number(value) === 0 || (Number(value) >= 5 && Number(value) <= 3600));

const monospace = { fontFamily: "monospace", fontSize: "0.85rem" };

const defaultShown = (value: string, defaults: string[] | string) => {
  const text = Array.isArray(defaults) ? defaults.join("\n") : defaults;
  return value.trim() !== "" || text === ""
    ? {}
    : { placeholder: text, slotProps: { inputLabel: { shrink: true } } };
};

function CertificateDetails({
  certificates,
}: {
  certificates: CertificateSummary[];
}) {
  return (
    <Stack spacing={1}>
      {certificates.map((cert) => (
        <Box
          key={cert.sha256_fingerprint}
          sx={{
            border: 1,
            borderColor: "divider",
            borderRadius: 1,
            p: 1.5,
            typography: "body2",
          }}
        >
          <Fields
            rows={[
              ["Subject", cert.subject],
              ["Issuer", cert.issuer],
              ["Identities", cert.identities.join(", ") || "—"],
              ["Valid From", formatTimestamp(cert.not_before)],
              ["Valid Until", formatTimestamp(cert.not_after)],
              ["SHA-256", cert.sha256_fingerprint],
            ]}
          />
        </Box>
      ))}
    </Stack>
  );
}

export default function SIPTrunkDialog({
  trunk,
  onClose,
}: {
  trunk?: SIPTrunk;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [name, setName] = useState(trunk?.name ?? "");
  const [domain, setDomain] = useState(trunk?.domain ?? "");
  const [addresses, setAddresses] = useState<AddressRow[]>(
    () =>
      trunk?.addresses.map((a) =>
        addressRow(
          a.host,
          a.port === undefined ? "" : String(a.port),
          a.transport ?? "",
        ),
      ) ?? [addressRow()],
  );
  const [inboundCidrs, setInboundCidrs] = useState(
    (trunk?.inbound_cidrs ?? []).join("\n"),
  );
  const [trusted, setTrusted] = useState(trunk?.trusted ?? false);
  const [enabled, setEnabled] = useState(trunk?.enabled ?? true);
  const [keepalive, setKeepalive] = useState(
    String(trunk?.keepalive_interval ?? DEFAULT_KEEPALIVE_INTERVAL),
  );
  const [tlsRequired, setTlsRequired] = useState(trunk?.tls?.required ?? false);
  const [initial] = useState(() => ({
    caCertificates: trunk?.tls?.ca_certificates ?? "",
    pins: (trunk?.tls?.pins ?? []).join("\n"),
  }));
  const [caCertificates, setCaCertificates] = useState(initial.caCertificates);
  const [pins, setPins] = useState(initial.pins);
  const [identities, setIdentities] = useState(
    (trunk?.tls?.identities ?? []).join("\n"),
  );

  const mutation = useMutation({
    mutationFn: (params: SIPTrunkParams) =>
      trunk ? updateSIPTrunk(trunk.id, params) : createSIPTrunk(params),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["sip-trunks"] });
      void queryClient.invalidateQueries({ queryKey: ["sip-routes"] });
      showSnackbar(
        trunk ? "Trunk updated successfully." : "Trunk created successfully.",
        "success",
      );
      onClose();
    },
  });

  const setAddress = (index: number, change: Partial<AddressRow>) =>
    setAddresses((rows) =>
      rows.map((row, i) => (i === index ? { ...row, ...change } : row)),
    );

  const caPem = caCertificates.trim();
  const pinList = splitPins(pins);
  const identityList = splitList(identities);
  const cidrs = splitList(inboundCidrs);
  const tls: SIPTrunkTLSParams = {
    required: tlsRequired,
    ...(caPem !== "" && { ca_certificates: caPem }),
    ...(pinList.length > 0 && { pins: pinList }),
    ...(identityList.length > 0 && { identities: identityList }),
  };

  const hasTls =
    tlsRequired ||
    caPem !== "" ||
    pinList.length > 0 ||
    identityList.length > 0;

  const params: SIPTrunkParams = {
    name: name.trim(),
    addresses: addresses.map((a) => ({
      host: a.host.trim(),
      ...(a.port !== "" && { port: Number(a.port) }),
      ...(a.transport !== "" && { transport: a.transport }),
    })),
    ...(cidrs.length > 0 && { inbound_cidrs: cidrs }),
    ...(domain.trim() !== "" && { domain: domain.trim() }),
    trusted,
    keepalive_interval: Number(keepalive),
    enabled,
    ...(hasTls && { tls }),
  };

  const defaults = trunkDefaults(
    params.addresses.map((a) => a.host),
    domain.trim(),
    caPem !== "" || pinList.length > 0,
  );

  const keepaliveError = validKeepalive(keepalive)
    ? undefined
    : "0, or 5 to 3600";
  const valid =
    params.name !== "" &&
    addresses.length > 0 &&
    addresses.every((a) => a.host.trim() !== "" && validPort(a.port)) &&
    keepaliveError === undefined;

  return (
    <EditDialog
      title={trunk ? "Edit Trunk" : "Create Trunk"}
      submitLabel={trunk ? "Update" : "Create"}
      pendingLabel={trunk ? "Updating…" : "Creating…"}
      failure={
        trunk ? "Could not update the trunk" : "Could not create the trunk"
      }
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate(params)}
      onClose={onClose}
    >
      <TextField
        label="Name"
        value={name}
        onChange={(e) => setName(e.target.value)}
        required
      />
      <TextField
        label="Domain"
        value={domain}
        onChange={(e) => setDomain(e.target.value)}
        {...defaultShown(domain, defaults.domain)}
      />
      <Stack spacing={1.5}>
        <FormLabel>Addresses</FormLabel>
        {addresses.map((address, index) => (
          <Stack
            key={address.key}
            direction="row"
            spacing={1}
            sx={{ alignItems: "flex-start" }}
          >
            <TextField
              label="Host"
              value={address.host}
              onChange={(e) => setAddress(index, { host: e.target.value })}
              placeholder="sip.example.com"
              required
              sx={{ flex: 1 }}
            />
            <TextField
              label="Port"
              value={address.port}
              onChange={(e) =>
                setAddress(index, { port: e.target.value.trim() })
              }
              error={!validPort(address.port)}
              sx={{ width: 100 }}
            />
            <TextField
              select
              label="Transport"
              value={address.transport}
              onChange={(e) =>
                setAddress(index, {
                  transport: e.target.value as SIPTransport | "",
                })
              }
              slotProps={{
                select: { displayEmpty: true },
                inputLabel: { shrink: true },
              }}
              sx={{ width: 120 }}
            >
              <MenuItem value="">Auto</MenuItem>
              {TRANSPORTS.map((t) => (
                <MenuItem key={t} value={t}>
                  {sipTransportLabels[t]}
                </MenuItem>
              ))}
            </TextField>
            <IconButton
              aria-label={`Remove address ${index + 1}`}
              onClick={() =>
                setAddresses((rows) => rows.filter((_, i) => i !== index))
              }
              disabled={addresses.length === 1}
              sx={{ mt: 1 }}
            >
              <DeleteIcon fontSize="small" />
            </IconButton>
          </Stack>
        ))}
        <Box>
          <Button
            size="small"
            startIcon={<AddIcon />}
            onClick={() => setAddresses((rows) => [...rows, addressRow()])}
          >
            Add Address
          </Button>
        </Box>
      </Stack>
      <TextField
        label="Inbound CIDRs"
        value={inboundCidrs}
        onChange={(e) => setInboundCidrs(e.target.value)}
        multiline
        minRows={2}
        {...defaultShown(inboundCidrs, defaults.inboundCidrs)}
      />
      <TextField
        label="Keepalive Interval (s)"
        value={keepalive}
        onChange={(e) => setKeepalive(e.target.value.trim())}
        error={keepaliveError !== undefined}
        helperText={keepaliveError}
        required
      />
      <FormGroup row>
        <FormControlLabel
          label="Enabled"
          control={
            <Checkbox
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
          }
        />
        <FormControlLabel
          label="Trusted"
          control={
            <Checkbox
              checked={trusted}
              onChange={(e) => setTrusted(e.target.checked)}
            />
          }
        />
      </FormGroup>
      <Accordion disableGutters variant="outlined">
        <AccordionSummary expandIcon={<ExpandMoreIcon />}>
          <Typography>TLS</Typography>
        </AccordionSummary>
        <AccordionDetails>
          <Stack spacing={2}>
            <FormControlLabel
              label="Required"
              control={
                <Checkbox
                  checked={tlsRequired}
                  onChange={(e) => setTlsRequired(e.target.checked)}
                />
              }
            />
            <TextField
              label="CA Certificates"
              value={caCertificates}
              onChange={(e) => setCaCertificates(e.target.value)}
              placeholder="-----BEGIN CERTIFICATE-----"
              multiline
              minRows={3}
              maxRows={10}
              slotProps={{ htmlInput: { sx: monospace } }}
            />
            {caCertificates === initial.caCertificates && (
              <CertificateDetails
                certificates={trunk?.tls?.ca_certificate_details ?? []}
              />
            )}
            <TextField
              label="Pins"
              value={pins}
              onChange={(e) => setPins(e.target.value)}
              multiline
              minRows={3}
              maxRows={10}
              slotProps={{ htmlInput: { sx: monospace } }}
            />
            <TextField
              label="Identities"
              value={identities}
              onChange={(e) => setIdentities(e.target.value)}
              multiline
              minRows={2}
              {...defaultShown(identities, defaults.identities)}
            />
          </Stack>
        </AccordionDetails>
      </Accordion>
    </EditDialog>
  );
}
