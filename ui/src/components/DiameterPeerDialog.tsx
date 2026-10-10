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
import { changesTransports, RESTART_WARNING } from "@/utils/restart";

export const DEFAULT_DIAMETER_PORT = 3868;

export const DEFAULT_PRIORITY = 10;

const APPLICATIONS: DiameterApplication[] = ["cx", "rx"];

const TRANSPORTS: DiameterTransport[] = ["tcp", "sctp"];

const sameApplications = (a: DiameterApplication[], b: DiameterApplication[]) =>
  a.length === b.length && a.every((app) => b.includes(app));

export default function DiameterPeerDialog({
  peer,
  peers,
  onClose,
}: {
  peer?: DiameterPeer;
  peers: DiameterPeer[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [host, setHost] = useState(peer?.host ?? "");
  const [address, setAddress] = useState(peer?.address ?? "");
  const [port, setPort] = useState(String(peer?.port ?? DEFAULT_DIAMETER_PORT));
  const [transport, setTransport] = useState<DiameterTransport>(
    peer?.transport ?? "tcp",
  );
  const [applications, setApplications] = useState<DiameterApplication[]>(
    peer?.applications ?? APPLICATIONS,
  );
  const [priority, setPriority] = useState(
    String(peer?.priority ?? DEFAULT_PRIORITY),
  );

  const mutation = useMutation({
    mutationFn: (params: DiameterPeerParams) =>
      peer ? updateDiameterPeer(peer.id, params) : createDiameterPeer(params),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["diameter-peers"] });
      void queryClient.invalidateQueries({ queryKey: ["diameter-routes"] });
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
  const priorityNumber = Number(priority);
  const params: DiameterPeerParams = {
    host: host.trim(),
    address: address.trim(),
    port: portNumber,
    transport,
    applications: ordered,
    priority: priorityNumber,
  };

  const portValid =
    /^\d+$/.test(port) && portNumber >= 1 && portNumber <= 65535;
  const priorityValid = /^\d+$/.test(priority) && priorityNumber <= 65535;
  const errors = {
    port: portValid ? undefined : "1 to 65535",
    priority: priorityValid ? undefined : "0 to 65535",
    applications: applications.length > 0 ? undefined : "At least one",
  };
  const valid =
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
        label="Address"
        value={address}
        onChange={(e) => setAddress(e.target.value)}
        placeholder="192.0.2.1"
        required
      />
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
      <TextField
        label="Priority"
        value={priority}
        onChange={(e) => setPriority(e.target.value.trim())}
        error={errors.priority !== undefined}
        helperText={
          errors.priority ??
          "Lowest first; peers of the same priority share requests."
        }
        required
      />
      <TextField
        label="Host"
        value={host}
        onChange={(e) => setHost(e.target.value)}
        helperText="Optional. Empty accepts the host the peer gives."
      />
      {valid && changesTransports(peers, peer, params) && (
        <Alert severity="warning">{RESTART_WARNING}</Alert>
      )}
    </EditDialog>
  );
}
