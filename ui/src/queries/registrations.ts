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
  // The other private identities registered with this public identity, which a request to it also reaches.
  registered_with: string[];
}

export interface RegisteredContact {
  contact: string;
  instance?: string;
  // The reg-id of a registration flow of the device (RFC 5626), absent for a contact that is not one.
  reg_id?: number;
  // The q-value the S-CSCF rings the contact by, 1 when it registered none (RFC 3841 §7.2.3).
  q: number;
  media: ("audio" | "video")[];
  registered_at: string;
  expires_at: string;
  address?: string;
  transport?: "udp" | "tcp";
  protected: boolean;
  signalling_path: SignallingPath;
}

// HSS is the HSS the S-CSCF sends a set's Cx requests to: the Origin-Host and Origin-Realm of its last answer.
export interface HSS {
  host: string;
  realm: string;
}

// ImplicitRegistrationSet is an implicit registration set: public identities that register and deregister as one,
// the contacts bound to them, and their HSS, absent while unknown.
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
