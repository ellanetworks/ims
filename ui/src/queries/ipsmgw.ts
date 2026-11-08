import { apiFetch } from "@/queries/utils";

export interface IPSMGW {
  number: string;
  name: string;
}

export const getIPSMGW = (): Promise<IPSMGW> =>
  apiFetch<IPSMGW>("/api/v1/ip-sm-gw");

export const updateIPSMGW = (number: string): Promise<IPSMGW> =>
  apiFetch<IPSMGW>("/api/v1/ip-sm-gw", { method: "PUT", body: { number } });

export interface SMSRouteParams {
  service_centre: string;
  peers: string[];
}

export interface SMSRoute extends SMSRouteParams {
  id: string;
}

const smsRouteUrl = (id: string) =>
  `/api/v1/ip-sm-gw/routes/${encodeURIComponent(id)}`;

export const listSMSRoutes = async (): Promise<SMSRoute[]> => {
  const { items } = await apiFetch<{ items: SMSRoute[] }>(
    "/api/v1/ip-sm-gw/routes",
  );
  return items;
};

export const createSMSRoute = (params: SMSRouteParams): Promise<SMSRoute> =>
  apiFetch<SMSRoute>("/api/v1/ip-sm-gw/routes", {
    method: "POST",
    body: params,
  });

export const updateSMSRoute = (
  id: string,
  params: SMSRouteParams,
): Promise<SMSRoute> =>
  apiFetch<SMSRoute>(smsRouteUrl(id), { method: "PUT", body: params });

export const deleteSMSRoute = async (id: string): Promise<void> => {
  await apiFetch(smsRouteUrl(id), { method: "DELETE" });
};

export const smsRouteName = (route: SMSRoute): string =>
  route.service_centre || "Default";
