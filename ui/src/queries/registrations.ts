import {
  apiFetch,
  withQuery,
  type Page,
  type PageParams,
} from "@/queries/utils";

export type SignallingPath = "unmonitored" | "monitored" | "lost";

export interface RegisteredIdentity {
  uri: string;
  display_name?: string;
  barred: boolean;
}

export interface RegisteredDevice {
  contact: string;
  instance?: string;
  media: ("audio" | "video")[];
  registered_at: string;
  expires_at: string;
  address?: string;
  transport?: "udp" | "tcp";
  protected: boolean;
  signalling_path: SignallingPath;
}

export interface Registration {
  impi: string;
  identities: RegisteredIdentity[];
  devices: RegisteredDevice[];
}

export interface ListRegistrationsParams extends PageParams {
  search?: string;
}

export interface Reauthentication {
  impi: string;
}

export const listRegistrations = (
  params: ListRegistrationsParams = {},
): Promise<Page<Registration>> =>
  apiFetch<Page<Registration>>(
    withQuery("/api/v1/registrations", {
      page: params.page,
      per_page: params.perPage,
      search: params.search,
    }),
  );

export const reauthenticate = (impi: string): Promise<Reauthentication> =>
  apiFetch<Reauthentication>(
    `/api/v1/registrations/${encodeURIComponent(impi)}/reauthenticate`,
    { method: "POST" },
  );
