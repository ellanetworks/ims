import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { AuthContext, authFor, type Auth } from "@/contexts/AuthContext";
import { SnackbarProvider } from "@/contexts/SnackbarContext";
import { RoleID } from "@/queries/users";

export const adminAuth = authFor("admin@example.com", RoleID.Admin);
export const readOnlyAuth = authFor("ro@example.com", RoleID.ReadOnly);

// renderWithClient renders ui logged in, as an admin unless auth says otherwise.
export const renderWithClient = (ui: ReactElement, auth: Auth = adminAuth) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return {
    client,
    ...render(
      <QueryClientProvider client={client}>
        <SnackbarProvider>
          <AuthContext.Provider value={auth}>{ui}</AuthContext.Provider>
        </SnackbarProvider>
      </QueryClientProvider>,
    ),
  };
};

export const stubApi = (
  routes: Record<string, (url: URL, init?: RequestInit) => Response>,
) => {
  return async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://ims.test");
    const route = routes[url.pathname];
    if (!route) {
      return new Response(JSON.stringify({ error: "not found" }), {
        status: 404,
      });
    }
    return route(url, init);
  };
};

export const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status });
