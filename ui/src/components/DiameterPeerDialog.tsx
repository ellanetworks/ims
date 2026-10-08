import { useState } from "react";
import {
  Alert,
  Checkbox,
  FormControl,
  FormControlLabel,
  FormGroup,
  FormHelperText,
  FormLabel,
  MenuItem,
  TextField,
} from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import {
  createDiameterPeer,
  type DiameterApplication,
  type DiameterPeer,
  type DiameterPeerParams,
  type DiameterTransport,
  updateDiameterPeer,
} from "@/queries/diameter";
import { applicationLabels, transportLabels } from "@/utils/labels";
import { RESTART_WARNING } from "@/utils/restart";

export const DEFAULT_DIAMETER_PORT = 3868;

const APPLICATIONS: DiameterApplication[] = ["cx", "rx"];

const TRANSPORTS: DiameterTransport[] = ["tcp", "sctp"];

const sameApplications = (a: DiameterApplication[], b: DiameterApplication[]) =>
  a.length === b.length && a.every((app) => b.includes(app));

// restarts reports whether saving a peer restarts Diameter and SIP: any change but its address or port does.
const restarts = (
  before: DiameterPeer | undefined,
  after: DiameterPeerParams,
) =>
  before === undefined ||
  before.host !== after.host ||
  before.realm !== after.realm ||
  before.transport !== after.transport ||
  before.applications.join() !== after.applications.join();

export default function DiameterPeerDialog({
  peer,
  onClose,
}: {
  peer?: DiameterPeer;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [host, setHost] = useState(peer?.host ?? "");
  const [realm, setRealm] = useState(peer?.realm ?? "");
  const [address, setAddress] = useState(peer?.address ?? "");
  const [port, setPort] = useState(String(peer?.port ?? DEFAULT_DIAMETER_PORT));
  const [transport, setTransport] = useState<DiameterTransport>(
    peer?.transport ?? "tcp",
  );
  const [applications, setApplications] = useState<DiameterApplication[]>(
    peer?.applications ?? APPLICATIONS,
  );

  const mutation = useMutation({
    mutationFn: (params: DiameterPeerParams) =>
      peer ? updateDiameterPeer(peer.id, params) : createDiameterPeer(params),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["diameter-peers"] });
      void queryClient.invalidateQueries({ queryKey: ["policy"] });
      onClose();
    },
  });

  const toggle = (app: DiameterApplication) =>
    setApplications((apps) =>
      apps.includes(app) ? apps.filter((a) => a !== app) : [...apps, app],
    );

  // The server compares applications in order, so an unchanged set keeps the stored order.
  const ordered =
    peer && sameApplications(applications, peer.applications)
      ? peer.applications
      : APPLICATIONS.filter((app) => applications.includes(app));

  const portNumber = Number(port);
  const params: DiameterPeerParams = {
    host: host.trim(),
    realm: realm.trim(),
    address: address.trim(),
    port: portNumber,
    transport,
    applications: ordered,
  };

  const portValid =
    /^\d+$/.test(port) && portNumber >= 1 && portNumber <= 65535;
  const errors = {
    port: portValid ? undefined : "1 to 65535",
    applications: applications.length > 0 ? undefined : "At least one",
  };
  const valid =
    params.host !== "" &&
    params.realm !== "" &&
    params.address !== "" &&
    Object.values(errors).every((e) => e === undefined);

  return (
    <EditDialog
      title={peer ? "Edit Peer" : "Add Peer"}
      submitLabel={peer ? "Update" : "Add"}
      pendingLabel={peer ? "Updating…" : "Adding…"}
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate(params)}
      onClose={onClose}
    >
      <TextField
        label="Host"
        value={host}
        onChange={(e) => setHost(e.target.value)}
        placeholder="hss.epc.mnc001.mcc001.3gppnetwork.org"
        required
      />
      <TextField
        label="Realm"
        value={realm}
        onChange={(e) => setRealm(e.target.value)}
        placeholder="epc.mnc001.mcc001.3gppnetwork.org"
        required
      />
      <TextField
        label="Address"
        value={address}
        onChange={(e) => setAddress(e.target.value)}
        placeholder="192.0.2.1"
        required
      />
      <TextField
        label="Port"
        value={port}
        onChange={(e) => setPort(e.target.value.trim())}
        error={errors.port !== undefined}
        helperText={errors.port}
        required
      />
      <TextField
        select
        label="Transport"
        value={transport}
        onChange={(e) => setTransport(e.target.value as DiameterTransport)}
      >
        {TRANSPORTS.map((t) => (
          <MenuItem key={t} value={t}>
            {transportLabels[t]}
          </MenuItem>
        ))}
      </TextField>
      <FormControl error={errors.applications !== undefined}>
        <FormLabel>Roles</FormLabel>
        <FormGroup row>
          {APPLICATIONS.map((app) => (
            <FormControlLabel
              key={app}
              label={applicationLabels[app]}
              control={
                <Checkbox
                  checked={applications.includes(app)}
                  onChange={() => toggle(app)}
                />
              }
            />
          ))}
        </FormGroup>
        {errors.applications && (
          <FormHelperText>{errors.applications}</FormHelperText>
        )}
      </FormControl>
      {valid && restarts(peer, params) && (
        <Alert severity="warning">{RESTART_WARNING}</Alert>
      )}
    </EditDialog>
  );
}
