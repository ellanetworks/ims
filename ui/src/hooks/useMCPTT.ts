import { useQuery } from "@tanstack/react-query";
import {
  getMCPTTStatus,
  getServiceConfiguration,
  listAffiliations,
  listClientRegistrations,
  listGroupCalls,
  listGroups,
  listUsers,
  mcpttKeys,
} from "@/queries/mcptt";
import { getOperator } from "@/queries/operator";
import { homeDomain } from "@/utils/operator";

// LIVE_REFRESH_MS is how often the live state is read again, as the IMS's pages do.
export const LIVE_REFRESH_MS = 5000;

// useMCPTTStatus is the status of the MCPTT AS: undefined while unknown, null if the IMS runs none. It is of the
// logged-in pages only: the route needs a login.
export function useMCPTTStatus() {
  const { data, error } = useQuery({
    queryKey: mcpttKeys.status,
    queryFn: getMCPTTStatus,
    staleTime: Infinity,
  });

  return { status: data, error };
}

// useMCPTTSettings are the settings of the AS, and the domains its identities are short of: the organisation's, of
// the service configuration, for the MCPTT and group IDs; the home domain, for the IMPUs.
export function useMCPTTSettings(enabled = true) {
  const config = useQuery({
    queryKey: mcpttKeys.serviceConfiguration,
    queryFn: getServiceConfiguration,
    enabled,
  });
  const users = useQuery({
    queryKey: mcpttKeys.users,
    queryFn: listUsers,
    enabled,
  });
  const groups = useQuery({
    queryKey: mcpttKeys.groups,
    queryFn: listGroups,
    enabled,
  });
  const operator = useQuery({
    queryKey: ["operator"],
    queryFn: getOperator,
    enabled,
  });

  return {
    config,
    users,
    groups,
    domain: config.data?.domain ?? "",
    homeDomain: operator.data
      ? homeDomain(operator.data.mcc, operator.data.mnc)
      : "",
  };
}

// useMCPTTLive is the live state of the AS, read again every LIVE_REFRESH_MS.
export function useMCPTTLive(enabled = true) {
  const live = { enabled, refetchInterval: LIVE_REFRESH_MS };

  return {
    registrations: useQuery({
      queryKey: mcpttKeys.registrations,
      queryFn: listClientRegistrations,
      ...live,
    }),
    affiliations: useQuery({
      queryKey: mcpttKeys.affiliations,
      queryFn: listAffiliations,
      ...live,
    }),
    calls: useQuery({
      queryKey: mcpttKeys.calls,
      queryFn: listGroupCalls,
      ...live,
    }),
  };
}
