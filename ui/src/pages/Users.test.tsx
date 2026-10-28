import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import UserDetail from "@/pages/UserDetail";
import Users from "@/pages/Users";
import { page } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const users = [
  { email: "admin@example.com", role_id: 1 },
  { email: "noc@example.com", role_id: 2 },
];

const serve = () => {
  const calls: { method: string; path: string; body?: unknown }[] = [];
  const record = (url: URL, init?: RequestInit) =>
    calls.push({
      method: String(init?.method),
      path: url.pathname,
      ...(init?.body ? { body: JSON.parse(String(init.body)) } : {}),
    });
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/users": (url, init) => {
          if (init?.method === "POST") {
            record(url, init);
            return json(201, { result: { message: "created" } });
          }
          return json(200, { result: page(users) });
        },
        "/api/v1/users/admin%40example.com": () =>
          json(200, { result: users[0] }),
        "/api/v1/users/noc%40example.com/password": (url, init) => {
          record(url, init);
          return json(200, { result: { message: "updated" } });
        },
        "/api/v1/users/noc%40example.com": (url, init) => {
          if (init?.method === "PUT") {
            record(url, init);
            return json(200, { result: { message: "updated" } });
          }
          if (init?.method === "DELETE") {
            record(url, init);
            return json(200, { result: { message: "deleted" } });
          }
          return json(200, { result: users[1] });
        },
        "/api/v1/users/admin%40example.com/api-tokens": () =>
          json(200, { result: page([]) }),
        "/api/v1/users/noc%40example.com/api-tokens": () =>
          json(200, { result: page([]) }),
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
      </Routes>
    </MemoryRouter>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Users", () => {
  it("lists the users, each a link to its page", async () => {
    serve();
    renderAt("/users");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Users (2)" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "noc@example.com" }),
    ).toHaveAttribute("href", "/users/noc%40example.com");
    expect(screen.getByText("Read Only")).toBeInTheDocument();
  });

  it("creates a user with a role", async () => {
    const calls = serve();
    renderAt("/users");

    fireEvent.click(await screen.findByRole("button", { name: "Create" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Email", { exact: false }), {
      target: { value: "nm@example.com" },
    });
    fireEvent.change(
      within(dialog).getByLabelText("Password", {
        exact: false,
        selector: "input",
      }),
      { target: { value: "correct horse" } },
    );
    fireEvent.mouseDown(within(dialog).getByRole("combobox", { name: "Role" }));
    fireEvent.click(screen.getByRole("option", { name: /^Network Manager/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(calls).toEqual([
      {
        method: "POST",
        path: "/api/v1/users",
        body: {
          email: "nm@example.com",
          password: "correct horse",
          role_id: 3,
        },
      },
    ]);
  });
});

describe("UserDetail", () => {
  it("does not let an admin delete itself or change its role or password here", async () => {
    serve();
    renderAt("/users/admin@example.com");

    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: /^Users\s*\/\s*admin@example\.com$/,
      }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Users" })).toHaveAttribute(
      "href",
      "/users",
    );
    expect(document.title).toBe("Users / admin@example.com · Ella IMS");

    expect(await screen.findByText("Admin")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Delete" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Edit role" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Change Password" }),
    ).toBeDisabled();
  });

  it("deletes another user after confirmation", async () => {
    const calls = serve();
    renderAt("/users/noc@example.com");

    expect(await screen.findByText("Read Only")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete",
      }),
    );

    expect(
      await screen.findByRole("heading", { level: 1, name: /^Users/ }),
    ).toBeInTheDocument();
    expect(calls).toEqual([
      { method: "DELETE", path: "/api/v1/users/noc%40example.com" },
    ]);
  });

  it("changes another user's role and sets its password", async () => {
    const calls = serve();
    renderAt("/users/noc@example.com");

    fireEvent.click(await screen.findByRole("button", { name: "Edit role" }));
    let dialog = screen.getByRole("dialog");
    fireEvent.mouseDown(within(dialog).getByRole("combobox", { name: "Role" }));
    fireEvent.click(screen.getByRole("option", { name: /^Network Manager/ }));
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );

    fireEvent.click(screen.getByRole("button", { name: "Change Password" }));
    dialog = screen.getByRole("dialog");
    expect(
      within(dialog).queryByLabelText("Current password", { exact: false }),
    ).not.toBeInTheDocument();
    fireEvent.change(
      within(dialog).getByLabelText("New password", {
        exact: false,
        selector: "input",
      }),
      { target: { value: "battery staple" } },
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Update" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );

    expect(calls).toEqual([
      {
        method: "PUT",
        path: "/api/v1/users/noc%40example.com",
        body: { role_id: 3 },
      },
      {
        method: "PUT",
        path: "/api/v1/users/noc%40example.com/password",
        body: { password: "battery staple" },
      },
    ]);
  });

  describe("Users notifications", () => {
    it("announces a created user", async () => {
      serve();
      renderAt("/users");

      fireEvent.click(await screen.findByRole("button", { name: "Create" }));
      const dialog = screen.getByRole("dialog");
      fireEvent.change(
        within(dialog).getByLabelText("Email", { exact: false }),
        {
          target: { value: "nm@example.com" },
        },
      );
      fireEvent.change(
        within(dialog).getByLabelText("Password", {
          exact: false,
          selector: "input",
        }),
        { target: { value: "correct horse" } },
      );
      fireEvent.click(within(dialog).getByRole("button", { name: "Create" }));

      expect(
        await screen.findByText("User created successfully."),
      ).toBeInTheDocument();
    });
  });
});
