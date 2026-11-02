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
  registered_with: string[];
}

export interface RegisteredContact {
  contact: string;
  instance?: string;
  reg_id?: number;
  q: number;
  media: ("audio" | "video")[];
  registered_at: string;
  expires_at: string;
  address?: string;
  transport?: "udp" | "tcp";
  protected: boolean;
  signalling_path: SignallingPath;
}

export interface HSS {
  host: string;
  realm: string;
}

export interface ImplicitRegistrationSet {
  hss?: HSS;
  identities: RegisteredIdentity[];
  contacts: RegisteredContact[];
}

export interface Registration {
  impi: string;
  implicit_registration_sets: ImplicitRegistrationSet[];
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
