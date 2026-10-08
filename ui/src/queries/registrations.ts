import { apiFetch } from "@/queries/utils";

export interface Reauthentication {
  impi: string;
}

export const reauthenticate = (impi: string): Promise<Reauthentication> =>
  apiFetch<Reauthentication>(
    `/api/v1/registrations/${encodeURIComponent(impi)}/reauthenticate`,
    { method: "POST" },
  );
