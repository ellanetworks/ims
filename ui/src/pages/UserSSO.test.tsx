import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import Profile from "@/pages/Profile";
import UserDetail from "@/pages/UserDetail";
import Users from "@/pages/Users";
import type { User } from "@/queries/users";
import { page } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const user = (overrides: Partial<User> = {}): User => ({
  email: "nm@example.com",
  role_id: 3,
  role_pinned: false,
  password_set: true,
  oidc_linked: true,
  ...overrides,
});

const oidcSettings = ({
  enabled = true,
  mapped = true,
}: { enabled?: boolean; mapped?: boolean } = {}) => ({
  enabled,
  external_url: "https://ims.example.com",
  redirect_url: "https://ims.example.com/api/v1/auth/oidc/callback",
  issuer: "https://idp.example.com/realms/ims",
  client_id: "ims",
  client_secret_set: true,
  display_name: "Keycloak",
  scopes: ["openid", "email"],
  session_lifetime_seconds: 86400,
  trust_unverified_email: false,
  ca_certificate: "",
  groups_claim: mapped ? "groups" : "",
  group_roles: mapped ? [{ group: "noc", role_id: 3 }] : [],
});

const serve = (
  u: User,
  me: User = user({ email: "admin@example.com", role_id: 1 }),
  oidc = oidcSettings(),
  fail?: { path: string; status: number; error: string },
) => {
  const calls: { method: string; path: string; body?: unknown }[] = [];
  const path = `/api/v1/users/${encodeURIComponent(u.email)}`;
  const record = (url: URL, init?: RequestInit) => {
    calls.push({
      method: String(init?.method),
      path: url.pathname,
      ...(init?.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
    if (fail?.path === url.pathname) {
      return json(fail.status, { error: fail.error });
    }
    return json(200, { result: { message: "ok" } });
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/users": (url, init) =>
          init?.method === "POST"
            ? record(url, init)
            : json(200, { result: page([me, u]) }),
        "/api/v1/users/me": () => json(200, { result: me }),
        "/api/v1/oidc": () => json(200, { result: oidc }),
        "/api/v1/users/me/api-tokens": () => json(200, { result: page([]) }),
        [path]: (url, init) =>
          init?.method === "PUT" ? record(url, init) : json(200, { result: u }),
        [`${path}/password`]: record,
        [`${path}/oidc`]: record,
        [`${path}/api-tokens`]: () => json(200, { result: page([]) }),
      }),
    ),
  );
  return calls;
};

const renderAt = (path: string) =>
  renderWithClient(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="/users" element={<Users />} />
        <Route path="/users/:email" element={<UserDetail />} />
        <Route path="/profile" element={<Profile />} />
      </Routes>
    </MemoryRouter>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Users and single sign-on", () => {
  it("lists how each user signs in, and pinned roles", async () => {
    serve(user({ password_set: false, role_pinned: true }));
    renderAt("/users");

    const grid = await screen.findByRole("grid", { name: "Users" });
    expect(await within(grid).findByText("Single sign-on")).toBeInTheDocument();
    expect(
      within(grid).getByText("Password, Single sign-on"),
    ).toBeInTheDocument();
    expect(
      within(grid).getByText("Network Manager (pinned)"),
    ).toBeInTheDocument();
  });

  it.each([
    ["linked and not pinned", user(), oidcSettings(), true],
    ["not linked", user({ oidc_linked: false }), oidcSettings(), false],
    ["off", user(), oidcSettings({ enabled: false }), false],
    ["without mappings", user(), oidcSettings({ mapped: false }), false],
  ])(
    "says a role comes from groups only when %s",
    async (_when, u, oidc, fromGroups) => {
      serve(u, undefined, oidc);
      const { client } = renderAt("/users");

      const grid = await screen.findByRole("grid", { name: "Users" });
      await waitFor(() => expect(client.getQueryData(["oidc"])).toBeDefined());
      expect(
        await within(grid).findByText(
          fromGroups ? "Network Manager (from groups)" : "Network Manager",
        ),
      ).toBeInTheDocument();
    },
  );

  it("shows on the account that a role comes from groups", async () => {
    serve(user());
    renderAt("/users/nm%40example.com");

    expect(await screen.findByText("From groups")).toBeInTheDocument();
    expect(screen.queryByText("Pinned")).not.toBeInTheDocument();
  });

  it("creates a user without a password", async () => {
    const calls = serve(user(), undefined, oidcSettings({ mapped: false }));
    renderAt("/users");

    fireEvent.click(await screen.findByRole("button", { name: "Create" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Email", { exact: false }), {
      target: { value: "sso@example.com" },
    });
    const create = within(dialog).getByRole("button", { name: "Create" });
    const passwordSwitch = await within(dialog).findByRole("switch", {
      name: "Password",
    });
    expect(passwordSwitch).toBeChecked();
    expect(create).toBeDisabled();
    fireEvent.click(passwordSwitch);
    expect(
      within(dialog).queryByLabelText(/^Password/, {
        selector: "input[type=password]",
      }),
    ).not.toBeInTheDocument();
    fireEvent.click(create);

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/v1/users",
          body: { email: "sso@example.com", password: "", role_id: 2 },
        },
      ]),
    );
  });

  it("needs a password when single sign-on is not configured", async () => {
    const oidc = {
      ...oidcSettings({ enabled: false, mapped: false }),
      issuer: "",
      client_id: "",
    };
    const calls = serve(user(), undefined, oidc);
    const { client } = renderAt("/users");

    await waitFor(() => expect(client.getQueryData(["oidc"])).toBeDefined());
    fireEvent.click(await screen.findByRole("button", { name: "Create" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).queryByRole("switch", { name: "Password" }),
    ).not.toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Email", { exact: false }), {
      target: { value: "pw@example.com" },
    });
    const create = within(dialog).getByRole("button", { name: "Create" });
    expect(create).toBeDisabled();
    fireEvent.change(
      within(dialog).getByLabelText("Password", {
        exact: false,
        selector: "input",
      }),
      { target: { value: "correct horse" } },
    );
    fireEvent.click(create);

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/v1/users",
          body: {
            email: "pw@example.com",
            password: "correct horse",
            role_id: 2,
          },
        },
      ]),
    );
  });

  it("pins the role of a user created with group mapping on", async () => {
    const calls = serve(user());
    renderAt("/users");

    fireEvent.click(await screen.findByRole("button", { name: "Create" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Email", { exact: false }), {
      target: { value: "boss@example.com" },
    });
    fireEvent.click(
      await within(dialog).findByRole("switch", { name: "Password" }),
    );
    fireEvent.mouseDown(within(dialog).getByRole("combobox", { name: /Role/ }));
    fireEvent.click(await screen.findByRole("option", { name: /Admin/ }));
    fireEvent.click(
      await within(dialog).findByRole("checkbox", { name: /Pin role/ }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "POST",
          path: "/api/v1/users",
          body: {
            email: "boss@example.com",
            password: "",
            role_id: 1,
            role_pinned: true,
          },
        },
      ]),
    );
  });

  it("removes a user's password", async () => {
    const calls = serve(user());
    renderAt("/users/nm%40example.com");

    fireEvent.click(
      await screen.findByRole("button", { name: "Remove Password" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(
      "They can then sign in only with single sign-on.",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Remove" }));

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "DELETE", path: "/api/v1/users/nm%40example.com/password" },
      ]),
    );
    expect(
      await screen.findByText("Password removed successfully."),
    ).toBeInTheDocument();
  });

  it.each([
    [
      "single sign-on is off",
      user(),
      oidcSettings({ enabled: false }),
      "Single sign-on is off, so they cannot sign in until it is on.",
    ],
    [
      "the user never signed in with single sign-on",
      user({ oidc_linked: false }),
      oidcSettings(),
      "They have not signed in with single sign-on yet.",
    ],
  ])(
    "warns before removing a password when %s",
    async (_case, u, oidc, warning) => {
      serve(u, undefined, oidc);
      renderAt("/users/nm%40example.com");

      fireEvent.click(
        await screen.findByRole("button", { name: "Remove Password" }),
      );
      await waitFor(() =>
        expect(screen.getByRole("dialog")).toHaveTextContent(warning),
      );
    },
  );

  it("says why a user could not be unlinked", async () => {
    serve(user(), undefined, oidcSettings(), {
      path: "/api/v1/users/nm%40example.com/oidc",
      status: 404,
      error: "This user is not linked to an identity provider account.",
    });
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Unlink" }));
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "Unlink",
      }),
    );

    expect(
      await screen.findByText(
        "Failed to unlink the user: This user is not linked to an identity provider account.",
      ),
    ).toBeInTheDocument();
  });

  it("offers to set a password to a user without one", async () => {
    serve(user({ password_set: false }));
    renderAt("/users/nm%40example.com");

    expect(
      await screen.findByRole("button", { name: "Set Password" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Remove Password" }),
    ).not.toBeInTheDocument();
  });

  it("unlinks a user from the identity provider", async () => {
    const calls = serve(user());
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Unlink" }));
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "Unlink",
      }),
    );

    await waitFor(() =>
      expect(calls).toEqual([
        { method: "DELETE", path: "/api/v1/users/nm%40example.com/oidc" },
      ]),
    );
  });

  it("pins the role of a user not linked yet", async () => {
    const calls = serve(user({ oidc_linked: false }));
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(
      await within(dialog).findByRole("checkbox", { name: /Pin role/ }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/v1/users/nm%40example.com",
          body: { role_id: 3, role_pinned: true },
        },
      ]),
    );
  });

  it("pins a role from groups when it is changed", async () => {
    const calls = serve(user());
    const { client } = renderAt("/users/nm%40example.com");

    await waitFor(() => expect(client.getQueryData(["oidc"])).toBeDefined());
    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    const pin = within(dialog).getByRole("checkbox", { name: /Pin role/ });
    const note = "Pinned, or groups would replace it at the next login.";
    const chooseRole = async (name: RegExp) => {
      fireEvent.mouseDown(
        within(dialog).getByRole("combobox", { name: /Role/ }),
      );
      fireEvent.click(await screen.findByRole("option", { name }));
    };

    expect(pin).not.toBeChecked();
    await chooseRole(/Read Only/);
    expect(pin).toBeChecked();
    expect(pin).toBeDisabled();
    expect(pin).toHaveAccessibleDescription(note);

    await chooseRole(/Network Manager/);
    expect(pin).not.toBeChecked();
    expect(pin).toBeEnabled();
    expect(within(dialog).queryByText(note)).not.toBeInTheDocument();

    await chooseRole(/Read Only/);
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/v1/users/nm%40example.com",
          body: { role_id: 2, role_pinned: true },
        },
      ]),
    );
  });

  it("shows the server's reason when a role comes from groups", async () => {
    serve(user());
    vi.stubGlobal(
      "fetch",
      vi.fn(
        stubApi({
          "/api/v1/users/nm%40example.com": (_url, init) =>
            init?.method === "PUT"
              ? json(409, {
                  error:
                    "This user's role comes from their identity provider groups. Pin the role to change it.",
                })
              : json(200, { result: user() }),
          "/api/v1/users/nm%40example.com/api-tokens": () =>
            json(200, { result: page([]) }),
        }),
      ),
    );
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.mouseDown(within(dialog).getByRole("combobox", { name: /Role/ }));
    fireEvent.click(await screen.findByRole("option", { name: /Read Only/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    expect(
      await within(dialog).findByText(/Pin the role to change it/),
    ).toBeInTheDocument();
  });

  it("lets a pinned role be unpinned without group mapping", async () => {
    const calls = serve(
      user({ role_pinned: true }),
      undefined,
      oidcSettings({ mapped: false }),
    );
    renderAt("/users/nm%40example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("checkbox", { name: /Pin role/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() =>
      expect(calls).toEqual([
        {
          method: "PUT",
          path: "/api/v1/users/nm%40example.com",
          body: { role_id: 3, role_pinned: false },
        },
      ]),
    );
  });

  it("does not offer pinning without group mapping", async () => {
    serve(user(), undefined, oidcSettings({ mapped: false }));
    const { client } = renderAt("/users/nm%40example.com");

    await waitFor(() => expect(client.getQueryData(["oidc"])).toBeDefined());
    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).queryByRole("checkbox", { name: /Pin role/ }),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).queryByRole("button", { name: "Unlink" }),
    ).not.toBeInTheDocument();
  });

  it("does not offer a password change to a user without a password", async () => {
    serve(
      user(),
      user({ email: "admin@example.com", role_id: 1, password_set: false }),
    );
    renderAt("/profile");

    expect(
      await screen.findByText(
        "You sign in with single sign-on, and have no password.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Change Password" }),
    ).not.toBeInTheDocument();
  });
});
