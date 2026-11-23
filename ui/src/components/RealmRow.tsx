import { Chip } from "@mui/material";
import DomainName from "@/components/DomainName";
import PeerStateChip from "@/components/PeerStateChip";
import type { SettingRow } from "@/components/SettingsTable";
import type { DiameterRoute } from "@/queries/diameter";
import { applicationLabels } from "@/utils/labels";

const defaultRealmLabel = (application: DiameterRoute["application"]) =>
  application === "sh" || application === "s6c" ? "HSS realm" : "home domain";

export function realmRow(
  route: DiameterRoute | undefined,
  application: DiameterRoute["application"],
  help: string,
  onEdit: ((route: DiameterRoute) => void) | undefined,
): SettingRow {
  return {
    label: `${applicationLabels[application]} Realm`,
    help: `${help} Default: the ${defaultRealmLabel(application)}.`,
    value: route && <DomainName name={route.destination_realm} />,
    onEdit: route && onEdit ? () => onEdit(route) : undefined,
  };
}

export function RouteStatus({ route }: { route: DiameterRoute | undefined }) {
  if (!route) return null;
  const state =
    route.peers.find((p) => p.status.state === "open")?.status.state ??
    route.peers[0]?.status.state;
  return state ? (
    <PeerStateChip state={state} />
  ) : (
    <Chip
      label={`no ${applicationLabels[route.application]} peer`}
      color="error"
      size="small"
    />
  );
}
