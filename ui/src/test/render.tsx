import type { ReactElement } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";

export const renderWithClient = (ui: ReactElement) => {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return {
    client,
    ...render(<QueryClientProvider client={client}>{ui}</QueryClientProvider>),
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
