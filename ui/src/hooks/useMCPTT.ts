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

export const LIVE_REFRESH_MS = 5000;

export function useMCPTTStatus() {
  const { data, error } = useQuery({
    queryKey: mcpttKeys.status,
    queryFn: getMCPTTStatus,
    staleTime: Infinity,
  });

  return { status: data, error };
}

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
