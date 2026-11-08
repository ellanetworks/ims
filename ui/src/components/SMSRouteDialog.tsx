import { useState } from "react";
import {
  Checkbox,
  FormControl,
  FormControlLabel,
  FormGroup,
  FormHelperText,
  FormLabel,
  TextField,
} from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { DEFAULT_PRIORITY } from "@/components/DiameterPeerDialog";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import { type DiameterPeer, peerName } from "@/queries/diameter";
import {
  createSMSRoute,
  type SMSRoute,
  type SMSRouteParams,
  updateSMSRoute,
} from "@/queries/ipsmgw";

const SERVICE_CENTRE = /^\d{0,15}$/;

export const smscPeers = (peers: DiameterPeer[]): DiameterPeer[] =>
  peers
    .filter((p) => p.applications.includes("sgd") && p.host !== "")
    .toSorted(
      (a, b) =>
        (a.priority ?? DEFAULT_PRIORITY) - (b.priority ?? DEFAULT_PRIORITY),
    );

export default function SMSRouteDialog({
  route,
  peers,
  onClose,
}: {
  route?: SMSRoute;
  peers: DiameterPeer[];
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [serviceCentre, setServiceCentre] = useState(
    route?.service_centre ?? "",
  );
  const [selected, setSelected] = useState<string[]>(route?.peers ?? []);

  const mutation = useMutation({
    mutationFn: (params: SMSRouteParams) =>
      route ? updateSMSRoute(route.id, params) : createSMSRoute(params),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["sms-routes"] });
      showSnackbar(
        route ? "Route updated successfully." : "Route created successfully.",
        "success",
      );
      onClose();
    },
  });

  const eligible = smscPeers(peers);
  const toggle = (id: string) =>
    setSelected((ids) =>
      ids.includes(id) ? ids.filter((i) => i !== id) : [...ids, id],
    );

  const serviceCentreError = SERVICE_CENTRE.test(serviceCentre)
    ? undefined
    : "Up to 15 digits, without +";
  const peersError = selected.length > 0 ? undefined : "At least one";
  const valid = serviceCentreError === undefined && peersError === undefined;

  return (
    <EditDialog
      title={route ? "Edit Route" : "Create Route"}
      submitLabel={route ? "Update" : "Create"}
      pendingLabel={route ? "Updating…" : "Creating…"}
      failure={
        route ? "Could not update the route" : "Could not create the route"
      }
      valid={valid}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() =>
        mutation.mutate({ service_centre: serviceCentre, peers: selected })
      }
      onClose={onClose}
    >
      <TextField
        label="Service Centre"
        value={serviceCentre}
        onChange={(e) => setServiceCentre(e.target.value.trim())}
        placeholder="Default"
        error={serviceCentreError !== undefined}
        helperText={
          serviceCentreError ??
          "The service centre address phones send to. Empty for the default route."
        }
        slotProps={{ inputLabel: { shrink: true } }}
      />
      <FormControl error={peersError !== undefined}>
        <FormLabel required>SMSC Peers</FormLabel>
        <FormGroup>
          {eligible.map((p) => (
            <FormControlLabel
              key={p.id}
              label={`${peerName(p)} (priority ${p.priority ?? DEFAULT_PRIORITY})`}
              control={
                <Checkbox
                  checked={selected.includes(p.id)}
                  onChange={() => toggle(p.id)}
                />
              }
            />
          ))}
        </FormGroup>
        <FormHelperText>
          {peersError ??
            "The SMS-IWMSC of the service centre: peers with the SGd role and a host. Tried by priority, lowest first."}
        </FormHelperText>
      </FormControl>
    </EditDialog>
  );
}
