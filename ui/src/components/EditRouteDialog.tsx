import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { type DiameterRoute, updateDiameterRoute } from "@/queries/diameter";
import { applicationLabels } from "@/utils/labels";

const DOMAIN_NAME =
  /^(?=.{1,253}$)([A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$/;

export default function EditRouteDialog({
  route,
  homeDomain,
  onClose,
}: {
  route: DiameterRoute;
  homeDomain: string;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const [realm, setRealm] = useState(route.realm);

  const mutation = useMutation({
    mutationFn: (next: string) => updateDiameterRoute(route.application, next),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["diameter-routes"] });
      onClose();
    },
  });

  const next = realm.trim();
  const error =
    next === "" || DOMAIN_NAME.test(next) ? undefined : "A domain name";

  return (
    <EditDialog
      title={`Edit ${applicationLabels[route.application]} Realm`}
      valid={error === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate(next)}
      onClose={onClose}
    >
      <TextField
        label="Realm"
        value={realm}
        onChange={(e) => setRealm(e.target.value)}
        placeholder={homeDomain}
        error={error !== undefined}
        helperText={error ?? "Empty for the home domain."}
      />
    </EditDialog>
  );
}
