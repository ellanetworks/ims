import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import SingleSignOn from "@/pages/SingleSignOn";
import type { OIDCSettings } from "@/queries/oidc";
import { authFor } from "@/contexts/AuthContext";
import { RoleID } from "@/queries/users";
import { adminAuth, json, renderWithClient, stubApi } from "@/test/render";

const settings = (overrides: Partial<OIDCSettings> = {}): OIDCSettings => ({
  enabled: true,
  external_url: "https://ims.example.com",
  redirect_url: "https://ims.example.com/api/v1/auth/oidc/callback",
  issuer: "https://idp.example.com/realms/ims",
  client_id: "ims",
  client_secret_set: true,
  display_name: "Keycloak",
  scopes: ["openid", "email", "profile"],
  session_lifetime_seconds: 86400,
  trust_unverified_email: false,
  ca_certificate: "",
  groups_claim: "groups",
  group_roles: [
    { group: "noc", role_id: 3 },
    { group: "ops", role_id: 2 },
  ],
  ...overrides,
});

const serve = ({
  current = settings(),
  discovery = () =>
    json(200, {
      result: {
        issuer: "https://idp.example.com/realms/ims",
        authorization_endpoint: "",
        token_endpoint: "",
        jwks_uri: "",
        signing_algorithms: ["RS256"],
        keys: 1,
        pkce: true,
        issuer_parameter: true,
        token_endpoint_auth_method: "client_secret_basic",
        userinfo_endpoint:
          "https://idp.example.com/realms/ims/protocol/openid-connect/userinfo",
      },
    }),
  put,
}: {
  current?: OIDCSettings;
  discovery?: () => Response;
  put?: () => Response;
} = {}) => {
  const bodies: { path: string; body: unknown }[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/oidc": (url, init) => {
          if (init?.method === "PUT") {
            const body = JSON.parse(String(init.body));
            bodies.push({ path: url.pathname, body });
            return put?.() ?? json(200, { result: { ...current, ...body } });
          }
          return json(200, { result: current });
        },
        "/api/v1/oidc/discovery": (url, init) => {
          bodies.push({
            path: url.pathname,
            body: JSON.parse(String(init?.body)),
          });
          return discovery();
        },
      }),
    ),
  );
  return bodies;
};

const renderPage = (auth = adminAuth) =>
  renderWithClient(
    <MemoryRouter>
      <SingleSignOn />
    </MemoryRouter>,
    auth,
  );

function field(label: string): HTMLElement;
function field(label: string, within: HTMLElement): HTMLElement | undefined;
function field(label: string, container?: HTMLElement) {
  if (container) {
    return (
      within(container).queryByLabelText(label, {
        exact: false,
        selector: "input, textarea",
      }) ?? undefined
    );
  }
  return screen.getByLabelText(label, {
    exact: false,
    selector: "input, textarea",
  });
}

const fill = (label: string, value: string) =>
  fireEvent.change(field(label), { target: { value } });

const bodyOf = (s: OIDCSettings) => ({
  enabled: s.enabled,
  external_url: s.external_url,
  issuer: s.issuer,
  client_id: s.client_id,
  client_secret: "",
  display_name: s.display_name,
  scopes: s.scopes,
  session_lifetime_seconds: s.session_lifetime_seconds,
  trust_unverified_email: s.trust_unverified_email,
  ca_certificate: s.ca_certificate,
  groups_claim: s.groups_claim,
  group_roles: s.group_roles,
});

const chooseUnit = async (dialog: HTMLElement, unit: string) => {
  fireEvent.mouseDown(within(dialog).getByRole("combobox", { name: "Unit" }));
  fireEvent.click(await screen.findByRole("option", { name: unit }));
};

const openEditor = async (name = "Edit provider") => {
  const edit = await screen.findByRole("button", { name });
  await waitFor(() => expect(edit).toBeEnabled());
  fireEvent.click(edit);
  return screen.findByRole("dialog");
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Single sign-on", () => {
  it("shows the settings and the redirect URL, never the secret", async () => {
    serve();
    renderPage();

    const provider = await screen.findByRole("table", { name: "Provider" });
    await within(provider).findByText("Keycloak");
    expect(
      within(provider).getByText(
        "https://ims.example.com/api/v1/auth/oidc/callback",
      ),
    ).toBeInTheDocument();
    expect(within(provider).getByText("Set")).toBeInTheDocument();
    expect(within(provider).getByText("1 day")).toBeInTheDocument();

    const groups = screen.getByRole("table", { name: "Roles from groups" });
    expect(within(groups).getByText("groups")).toBeInTheDocument();
    expect(
      within(groups).getByText("1. noc → Network Manager"),
    ).toBeInTheDocument();
    expect(within(groups).getByText("2. ops → Read Only")).toBeInTheDocument();
  });

  it("links to the how-to", async () => {
    serve();
    renderPage();

    expect(
      await screen.findByRole("link", { name: "Configure Single Sign-On" }),
    ).toHaveAttribute(
      "href",
      "https://docs.ellanetworks.com/ims/how_to/configure_single_sign_on/",
    );
  });

  it("asks for the external URL before showing a redirect URL", async () => {
    serve({
      current: settings({ enabled: false, external_url: "", redirect_url: "" }),
    });
    renderPage();

    expect(
      await screen.findByText("Set the external URL first."),
    ).toBeInTheDocument();
  });

  it("saves the provider, keeping the secret and the groups", async () => {
    const bodies = serve();
    renderPage();

    const dialog = await openEditor();
    expect(
      within(dialog).getByRole("heading", { name: "Edit Provider" }),
    ).toBeInTheDocument();
    expect(field("Groups claim", dialog)).toBeUndefined();
    fill("Display name", "Corporate SSO");
    await chooseUnit(dialog, "hours");
    fill("Session lifetime", "8");
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0].body).toEqual({
      enabled: true,
      external_url: "https://ims.example.com",
      issuer: "https://idp.example.com/realms/ims",
      client_id: "ims",
      client_secret: "",
      display_name: "Corporate SSO",
      scopes: ["openid", "email", "profile"],
      session_lifetime_seconds: 28800,
      trust_unverified_email: false,
      ca_certificate: "",
      groups_claim: "groups",
      group_roles: [
        { group: "noc", role_id: 3 },
        { group: "ops", role_id: 2 },
      ],
    });
  });

  it("saves the roles from groups, keeping the provider", async () => {
    const bodies = serve({
      current: settings({
        trust_unverified_email: true,
        ca_certificate: "-----BEGIN CERTIFICATE-----",
      }),
    });
    renderPage();

    const dialog = await openEditor("Edit roles from groups");
    expect(
      within(dialog).getByRole("heading", { name: "Edit Roles from Groups" }),
    ).toBeInTheDocument();
    expect(field("Issuer URL", dialog)).toBeUndefined();
    fill("Groups claim", " roles ");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Move group 2 up" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0].body).toEqual({
      enabled: true,
      external_url: "https://ims.example.com",
      issuer: "https://idp.example.com/realms/ims",
      client_id: "ims",
      client_secret: "",
      display_name: "Keycloak",
      scopes: ["openid", "email", "profile"],
      session_lifetime_seconds: 86400,
      trust_unverified_email: true,
      ca_certificate: "-----BEGIN CERTIFICATE-----",
      groups_claim: "roles",
      group_roles: [
        { group: "ops", role_id: 2 },
        { group: "noc", role_id: 3 },
      ],
    });
  });

  it("shows the redirect URL as the external URL is typed", async () => {
    serve({
      current: settings({ enabled: false, external_url: "", redirect_url: "" }),
    });
    renderPage();

    const dialog = await openEditor();
    expect(field("Redirect URL", dialog)).toBeUndefined();
    fill("External URL", "https://ims.example.org");
    expect(field("Redirect URL", dialog)).toHaveValue(
      "https://ims.example.org/api/v1/auth/oidc/callback",
    );
    fill("External URL", "https://ims.example.org/");
    expect(field("Redirect URL", dialog)).toHaveValue(
      "https://ims.example.org/api/v1/auth/oidc/callback",
    );
    expect(
      within(dialog).getByRole("button", { name: "Copy the redirect URL" }),
    ).toBeInTheDocument();
    fill("External URL", "https://ims.example.org/ui");
    expect(field("Redirect URL", dialog)).toBeUndefined();
  });

  it("warns that changing the issuer signs everyone out", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    const warning =
      "Saving signs out everyone who logged in with single sign-on.";
    expect(
      within(dialog).queryByRole("switch", { name: "Enabled" }),
    ).not.toBeInTheDocument();
    fill("Client ID", "other");
    expect(within(dialog).queryByText(warning)).not.toBeInTheDocument();
    fill("Issuer URL", "https://idp.example.com/realms/other");
    expect(within(dialog).getByText(warning)).toBeInTheDocument();
  });

  it("does not warn about the issuer while single sign-on is off", async () => {
    serve({ current: settings({ enabled: false }) });
    renderPage();

    const dialog = await openEditor();
    fill("Issuer URL", "https://idp.example.com/realms/other");
    expect(
      within(dialog).queryByText(/Saving signs out everyone/),
    ).not.toBeInTheDocument();
  });

  it("enables single sign-on from the page", async () => {
    const bodies = serve({ current: settings({ enabled: false }) });
    renderPage();

    const enabled = await screen.findByRole("switch", { name: "Enabled" });
    expect(enabled).not.toBeChecked();
    fireEvent.click(enabled);

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0].body).toEqual({
      ...bodyOf(settings()),
      enabled: true,
    });
    expect(
      await screen.findByText("Single sign-on enabled successfully."),
    ).toBeInTheDocument();
    await waitFor(() => expect(enabled).toBeChecked());
  });

  it("says what is missing before single sign-on can be enabled", async () => {
    serve({
      current: settings({
        enabled: false,
        external_url: "",
        redirect_url: "",
        client_secret_set: false,
      }),
    });
    renderPage();

    const enabled = await screen.findByRole("switch", { name: "Enabled" });
    expect(enabled).toBeDisabled();
    fireEvent.mouseOver(enabled.closest("label")!);
    expect(
      await screen.findByRole("tooltip", {
        name: "Missing: external URL, client secret.",
      }),
    ).toBeInTheDocument();
  });

  it("shows why single sign-on could not be enabled", async () => {
    serve({
      current: settings({ enabled: false }),
      put: () => json(400, { error: "Invalid single sign-on settings" }),
    });
    renderPage();

    fireEvent.click(await screen.findByRole("switch", { name: "Enabled" }));
    expect(
      await screen.findByText(
        "Failed to enable single sign-on: Invalid single sign-on settings",
      ),
    ).toBeInTheDocument();
  });

  it("disables single sign-on after confirmation", async () => {
    const bodies = serve();
    renderPage();

    fireEvent.click(await screen.findByRole("switch", { name: "Enabled" }));
    const dialog = await screen.findByRole("dialog");
    expect(dialog).toHaveTextContent(
      "Disable single sign-on? Everyone who logged in with single sign-on is signed out.",
    );
    expect(bodies).toHaveLength(0);
    fireEvent.click(within(dialog).getByRole("button", { name: "Disable" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0].body).toEqual({ ...bodyOf(settings()), enabled: false });
    expect(
      await screen.findByText("Single sign-on disabled successfully."),
    ).toBeInTheDocument();
  });

  it("lets only who can change the settings enable or disable", async () => {
    serve();
    const auth = authFor("ro@example.com", RoleID.Admin);
    renderPage({ ...auth, can: (p) => p !== "oidc:update" && auth.can(p) });

    await screen.findByText("Keycloak");
    const enabled = await screen.findByRole("switch", { name: "Enabled" });
    expect(enabled).toBeChecked();
    expect(enabled).toBeDisabled();
  });

  it("refuses settings the API would refuse", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    const update = within(dialog).getByRole("button", { name: "Update" });

    for (const [label, value, message] of [
      ["Display name", "x".repeat(65), "At most 64 bytes."],
      ["Display name", "Key\u0007cloak", "No control characters."],
      ["Client ID", "x".repeat(256), "At most 255 bytes."],
      ["Issuer URL", "http://idp.example.com", "An https URL."],
      ["Issuer URL", "https:idp.example.com", "An https URL."],
      [
        "Issuer URL",
        "https://idp.example.com/realms/ims?x=1",
        "With no query or fragment.",
      ],
      [
        "Issuer URL",
        "https://user:pass@idp.example.com/realms/ims",
        "With no user name or password.",
      ],
      [
        "Issuer URL",
        `https://idp.example.com/${"x".repeat(2048)}`,
        "At most 2048 bytes.",
      ],
      ["External URL", "https://ims.example.com/ui", "With no path."],
      ["External URL", "https://ims.example.com//", "With no path."],
      [
        "External URL",
        "https://ims.example.com#top",
        "With no query or fragment.",
      ],
      ["Scopes", "email profile", "Include openid and email."],
      ["Scopes", "openid profile", "Include openid and email."],
      ["Scopes", "openid email email", "email appears more than once."],
      ["Scopes", 'openid email "groups"', '"groups" is not a valid scope.'],
      [
        "Scopes",
        [
          "openid",
          "email",
          ...Array.from({ length: 19 }, (_, i) => `s${i}`),
        ].join(" "),
        "At most 20 scopes.",
      ],
      ["Session lifetime", "0", "From 5 minutes to 30 days."],
      ["Session lifetime", "31", "From 5 minutes to 30 days."],
      ["Session lifetime", "1.5", "From 5 minutes to 30 days."],
      [
        "CA certificate",
        "not a certificate",
        "PEM certificates, each starting with -----BEGIN CERTIFICATE-----.",
      ],
    ] as const) {
      const before = (field(label) as HTMLInputElement).value;
      fill(label, value);
      expect(await within(dialog).findByText(message)).toBeInTheDocument();
      expect(update).toBeDisabled();
      fill(label, before);
      await waitFor(() => expect(update).toBeEnabled());
    }
  });

  it("shows each group's error on its field", async () => {
    serve();
    renderPage();

    const dialog = await openEditor("Edit roles from groups");
    const update = within(dialog).getByRole("button", { name: "Update" });

    fill("Groups claim", "x".repeat(256));
    expect(field("Groups claim")).toHaveAccessibleDescription(
      "At most 255 bytes.",
    );
    expect(update).toBeDisabled();
    fill("Groups claim", "");
    expect(field("Groups claim")).toHaveAccessibleDescription(
      "Enter the claim that holds the groups.",
    );
    fill("Groups claim", "groups");

    fireEvent.click(within(dialog).getByRole("button", { name: "Add group" }));
    expect(field("Group 3")).toHaveAccessibleDescription("Enter the group.");
    expect(field("Group 3")).toBeInvalid();
    expect(update).toBeDisabled();

    fill("Group 3", " noc ");
    expect(field("Group 3")).toHaveAccessibleDescription(
      "Appears more than once.",
    );
    expect(field("Group 1")).toBeValid();
    expect(update).toBeDisabled();

    fill("Group 3", "x".repeat(256));
    expect(field("Group 3")).toHaveAccessibleDescription("At most 255 bytes.");

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove group 3" }),
    );
    await waitFor(() => expect(update).toBeEnabled());
  });

  it("keeps the focus on the group moved", async () => {
    serve();
    renderPage();

    const dialog = await openEditor("Edit roles from groups");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Move group 2 up" }),
    );
    expect(field("Group 1")).toHaveValue("ops");
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "Move group 1 down" }),
      ).toHaveFocus(),
    );

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Move group 1 down" }),
    );
    expect(field("Group 2")).toHaveValue("ops");
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "Move group 2 up" }),
      ).toHaveFocus(),
    );
  });

  it.each([
    [300, "5", "minutes", "5 minutes"],
    [600, "10", "minutes", "10 minutes"],
    [5400, "90", "minutes", "1 hour 30 minutes"],
    [28800, "8", "hours", "8 hours"],
    [86400, "1", "days", "1 day"],
    [301, "301", "seconds", "5 minutes 1 second"],
  ])(
    "keeps a session lifetime of %i seconds unchanged",
    async (seconds, value, unit, shown) => {
      const bodies = serve({
        current: settings({ session_lifetime_seconds: seconds }),
      });
      renderPage();

      expect(await screen.findByText(shown)).toBeInTheDocument();
      const dialog = await openEditor();
      expect(field("Session lifetime")).toHaveValue(Number(value));
      expect(
        within(dialog).getByRole("combobox", { name: "Unit" }),
      ).toHaveTextContent(unit);
      fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

      await waitFor(() => expect(bodies).toHaveLength(1));
      expect(bodies[0].body).toMatchObject({
        session_lifetime_seconds: seconds,
      });
    },
  );

  it("sets the session lifetime in minutes, hours or days", async () => {
    const bodies = serve();
    renderPage();

    const dialog = await openEditor();
    await chooseUnit(dialog, "minutes");
    fill("Session lifetime", "5");
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    await waitFor(() => expect(bodies).toHaveLength(1));
    expect(bodies[0].body).toMatchObject({ session_lifetime_seconds: 300 });
  });

  it("needs the client secret again when the issuer or client ID changes", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    const update = within(dialog).getByRole("button", { name: "Update" });
    const message =
      "Enter the client secret again: the issuer URL or client ID changed.";

    for (const [label, value] of [
      ["Issuer URL", "https://evil.example.com"],
      ["Client ID", "other"],
    ] as const) {
      const before = (field(label) as HTMLInputElement).value;
      fill(label, value);
      expect(await within(dialog).findByText(message)).toBeInTheDocument();
      expect(update).toBeDisabled();
      fill("Client secret", "new-secret");
      await waitFor(() => expect(update).toBeEnabled());
      fill("Client secret", "");
      fill(label, before);
      await waitFor(() => expect(update).toBeEnabled());
    }
  });

  it("brings the server's error into view", async () => {
    serve({
      put: () => json(400, { error: "Invalid single sign-on settings" }),
    });
    renderPage();

    const dialog = await openEditor();
    const content = dialog.querySelector(".MuiDialogContent-root")!;
    content.scrollTop = 900;
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));

    expect(
      await within(dialog).findByText(
        "Could not update: Invalid single sign-on settings",
      ),
    ).toBeInTheDocument();
    expect(content.scrollTop).toBe(0);
  });

  it("does not prefill an http external URL", async () => {
    serve({
      current: settings({ enabled: false, external_url: "", redirect_url: "" }),
    });
    renderPage();

    await openEditor();
    expect(field("External URL")).toHaveValue("");
    expect(field("External URL")).toBeValid();
  });

  it("offers Edit only to who can change the settings", async () => {
    serve();
    const auth = authFor("ro@example.com", RoleID.Admin);
    renderPage({ ...auth, can: (p) => p !== "oidc:update" && auth.can(p) });

    await screen.findByText("Keycloak");
    expect(
      screen.queryByRole("button", { name: /^Edit/ }),
    ).not.toBeInTheDocument();
  });

  it("does not ask for a secret while none is stored and single sign-on is off", async () => {
    serve({
      current: settings({ enabled: false, client_secret_set: false }),
    });
    renderPage();

    const dialog = await openEditor();
    fill("Issuer URL", "https://other.example.com");
    expect(
      within(dialog).queryByText(/Enter the client secret/),
    ).not.toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Update" }),
    ).toBeEnabled();
  });

  it("needs a complete provider while single sign-on is on", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    fill("Display name", "");
    expect(
      await within(dialog).findByText(
        "Enter the name to show on the login page.",
      ),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("button", { name: "Update" }),
    ).toBeDisabled();
  });

  it("tests the identity provider before saving", async () => {
    const bodies = serve();
    renderPage();

    const dialog = await openEditor();
    fireEvent.click(within(dialog).getByRole("button", { name: "Test" }));

    const result = await within(dialog).findByRole("status");
    expect(await within(result).findByText("Works")).toBeInTheDocument();
    expect(result).not.toHaveTextContent("Failed");
    expect(bodies).toEqual([
      {
        path: "/api/v1/oidc/discovery",
        body: {
          issuer: "https://idp.example.com/realms/ims",
          ca_certificate: "",
        },
      },
    ]);
  });

  it("shows why the identity provider cannot be used", async () => {
    serve({
      discovery: () =>
        json(502, { error: "the identity provider: discovery: 404 Not Found" }),
    });
    renderPage();

    const dialog = await openEditor();
    fireEvent.click(within(dialog).getByRole("button", { name: "Test" }));

    const result = await within(dialog).findByRole("status");
    expect(await within(result).findByText("Failed")).toBeInTheDocument();
    expect(
      within(result).getByText(
        "the identity provider: discovery: 404 Not Found",
      ),
    ).toBeInTheDocument();
  });

  it("clears the test result when the issuer or CA certificate changes", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    for (const [label, value] of [
      ["Issuer URL", "https://idp.example.com/realms/other"],
      ["CA certificate", "-----BEGIN CERTIFICATE-----"],
    ] as const) {
      fireEvent.click(within(dialog).getByRole("button", { name: "Test" }));
      expect(await within(dialog).findByText("Works")).toBeInTheDocument();
      fill(label, value);
      expect(within(dialog).queryByText("Works")).not.toBeInTheDocument();
    }
  });

  it("explains the risk of trusting email addresses on hover", async () => {
    serve();
    renderPage();

    const dialog = await openEditor();
    const trust = within(dialog).getByRole("switch", {
      name: "Trust email addresses from this provider",
    });
    expect(within(dialog).queryByRole("alert")).not.toBeInTheDocument();

    fireEvent.mouseOver(trust);

    expect(
      await screen.findByRole("tooltip", {
        name: /can log in as the Ella IMS user with that address, Admins included/,
      }),
    ).toBeInTheDocument();
  });

  it("shows the enabled state as a switch outside the provider settings", async () => {
    serve();
    renderPage();

    expect(
      await screen.findByRole("switch", { name: "Enabled" }),
    ).toBeChecked();
    expect(
      within(screen.getByRole("table", { name: "Provider" })).queryByText(
        "Status",
      ),
    ).not.toBeInTheDocument();
  });
});
