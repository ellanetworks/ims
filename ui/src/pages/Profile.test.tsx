import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import Profile from "@/pages/Profile";
import type { APIToken } from "@/queries/api_tokens";
import { page } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const token: APIToken = {
  id: "abcdefghijkl",
  name: "OTA platform",
  created_at: "2026-09-29T10:00:00.000Z",
  last_used_at: "2026-09-30T10:00:00.000Z",
};

const renderProfile = (path = "/profile") =>
  renderWithClient(
    <MemoryRouter initialEntries={[path]}>
      <Profile />
    </MemoryRouter>,
  );

const serve = () => {
  const calls: { method: string; path: string; body?: unknown }[] = [];
  let tokens = [token];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/users/me": () =>
          json(200, { result: { email: "admin@example.com", role_id: 1 } }),
        "/api/v1/users/me/api-tokens": (url, init) => {
          if (init?.method === "POST") {
            calls.push({
              method: "POST",
              path: url.pathname,
              body: JSON.parse(String(init.body)),
            });
            tokens = [
              ...tokens,
              { ...token, id: "mnopqrstuvwx", name: "CI pipeline" },
            ];
            return json(201, {
              result: { token: "ellasmsc_mnopqrstuvwx_SECRET" },
            });
          }
          return json(200, { result: page(tokens) });
        },
        "/api/v1/users/me/api-tokens/abcdefghijkl": (url, init) => {
          calls.push({ method: String(init?.method), path: url.pathname });
          tokens = tokens.filter((t) => t.id !== "abcdefghijkl");
          return json(200, { result: { message: "deleted" } });
        },
        "/api/v1/users/me/password": (url, init) => {
          if (JSON.parse(String(init?.body)).current_password === "wrong") {
            return json(400, { error: "Current password is incorrect" });
          }
          calls.push({
            method: String(init?.method),
            path: url.pathname,
            body: JSON.parse(String(init?.body)),
          });
          return json(200, { result: { message: "updated" } });
        },
      }),
    ),
  );
  return calls;
};

const fill = (label: string, value: string) =>
  fireEvent.change(
    screen.getByLabelText(label, { exact: false, selector: "input" }),
    { target: { value } },
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Profile", () => {
  it("shows the account and its API tokens", async () => {
    serve();
    renderProfile();

    expect(await screen.findByText("admin@example.com")).toBeInTheDocument();
    expect(screen.getByText("Admin")).toBeInTheDocument();
    const row = within(await screen.findByRole("table", { name: "API tokens" }))
      .getByText("OTA platform")
      .closest("tr") as HTMLElement;
    expect(within(row).getByText("Active")).toBeInTheDocument();
    expect(within(row).getByText("Never")).toBeInTheDocument();
  });

  it("creates an API token, and shows it once", async () => {
    const calls = serve();
    renderProfile();

    fireEvent.click(
      await screen.findByRole("button", { name: "Create Token" }),
    );
    fill("Name", "CI pipeline");
    fireEvent.click(screen.getByRole("checkbox", { name: "No expiry" }));
    fireEvent.click(screen.getByRole("button", { name: "Create" }));

    expect(await screen.findByTestId("new-api-token")).toHaveTextContent(
      "ellasmsc_mnopqrstuvwx_SECRET",
    );
    expect(calls).toEqual([
      {
        method: "POST",
        path: "/api/v1/users/me/api-tokens",
        body: { name: "CI pipeline" },
      },
    ]);
    expect(await screen.findByText("CI pipeline")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByTestId("new-api-token")).not.toBeInTheDocument();
  });

  it("deletes an API token after confirmation", async () => {
    const calls = serve();
    renderProfile();

    fireEvent.click(
      await screen.findByRole("button", { name: "Delete OTA platform" }),
    );
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete",
      }),
    );

    await waitFor(() =>
      expect(screen.queryByText("OTA platform")).not.toBeInTheDocument(),
    );
    expect(calls).toEqual([
      { method: "DELETE", path: "/api/v1/users/me/api-tokens/abcdefghijkl" },
    ]);
  });

  it("changes the password with the current one", async () => {
    const calls = serve();
    renderProfile();

    fireEvent.click(
      await screen.findByRole("button", { name: "Change Password" }),
    );
    fill("Current password", "correct horse");
    fill("New password", "battery staple");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(calls).toEqual([
      {
        method: "PUT",
        path: "/api/v1/users/me/password",
        body: { current_password: "correct horse", password: "battery staple" },
      },
    ]);
  });

  it("shows a wrong current password, and keeps the dialog", async () => {
    const calls = serve();
    renderProfile();

    fireEvent.click(
      await screen.findByRole("button", { name: "Change Password" }),
    );
    fill("Current password", "wrong");
    fill("New password", "battery staple");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    expect(
      await within(screen.getByRole("dialog")).findByText(
        /Current password is incorrect/,
      ),
    ).toBeInTheDocument();
    expect(calls).toEqual([]);
  });
});

describe("Profile notifications", () => {
  it("says why a token could not be deleted", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/users/me": () =>
            json(200, { result: { email: "admin@example.com", role_id: 1 } }),
          "/api/v1/users/me/api-tokens": () =>
            json(200, { result: page([token]) }),
          "/api/v1/users/me/api-tokens/abcdefghijkl": () =>
            json(500, { error: "database is locked" }),
        }),
      ),
    );
    renderProfile();

    fireEvent.click(
      await screen.findByRole("button", { name: "Delete OTA platform" }),
    );
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete",
      }),
    );

    expect(
      await screen.findByText("Failed to delete API token: database is locked"),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("lets the error of a dialog be dismissed", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/users/me": () =>
            json(200, { result: { email: "admin@example.com", role_id: 1 } }),
          "/api/v1/users/me/api-tokens": () =>
            json(200, { result: page([token]) }),
          "/api/v1/users/me/password": () =>
            json(400, { error: "Current password is incorrect" }),
        }),
      ),
    );
    renderProfile();

    fireEvent.click(
      await screen.findByRole("button", { name: "Change Password" }),
    );
    fill("Current password", "wrong");
    fill("New password", "battery staple");
    fireEvent.click(screen.getByRole("button", { name: "Update" }));

    const dialog = screen.getByRole("dialog");
    await within(dialog).findByText(/Current password is incorrect/);
    fireEvent.click(within(dialog).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(
        within(dialog).queryByText(/Current password is incorrect/),
      ).not.toBeInTheDocument(),
    );
  });
});

describe("Profile single sign-on link", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    sessionStorage.clear();
  });

  const serveLink = (user: Record<string, unknown>) =>
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/users/me": () =>
            json(200, {
              result: {
                email: "nm@example.com",
                role_id: 3,
                password_set: true,
                ...user,
              },
            }),
          "/api/v1/users/me/api-tokens": () => json(200, { result: page([]) }),
          "/api/v1/auth/oidc": () =>
            json(200, { result: { enabled: true, display_name: "Keycloak" } }),
        }),
      ),
    );

  it("offers to link an account that is not linked", async () => {
    serveLink({ oidc_linked: false });
    renderProfile();

    const link = await screen.findByRole("link", {
      name: "Link Keycloak account",
    });
    expect(link).toHaveAttribute("href", "/api/v1/auth/oidc/login?link=1");
  });

  it("does not offer to link a linked account", async () => {
    serveLink({ oidc_linked: true });
    renderProfile();

    expect(await screen.findByText("nm@example.com")).toBeInTheDocument();
    expect(
      screen.queryByRole("link", { name: /Link Keycloak account/ }),
    ).not.toBeInTheDocument();
  });

  it("says why a link failed", async () => {
    serveLink({ oidc_linked: false });
    renderProfile("/profile?error=linked_elsewhere");

    expect(
      await screen.findByText(
        "That account at your identity provider, or yours, is already linked.",
      ),
    ).toBeInTheDocument();
  });

  it("says a link succeeded", async () => {
    sessionStorage.setItem("ims.oidcLinking", "1");
    serveLink({ oidc_linked: true });
    renderProfile();

    expect(
      await screen.findByText(/Your account is linked/),
    ).toBeInTheDocument();
    expect(sessionStorage.getItem("ims.oidcLinking")).toBeNull();
  });
});
