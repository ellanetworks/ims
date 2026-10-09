import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import App from "@/App";
import { identity, operator, policy, sip } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = () =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/status": () =>
          json(200, { result: { version: "v0.0.1", revision: "" } }),
        "/api/v1/operator": () => json(200, { result: operator }),
        "/api/v1/sip": () => json(200, { result: sip }),
        "/api/v1/diameter": () => json(200, { result: identity }),
        "/api/v1/diameter/peers": () => json(200, { result: { items: [] } }),
        "/api/v1/policy": () => json(200, { result: policy() }),
        "/api/v1/registrations": () =>
          json(200, {
            result: { items: [], page: 1, per_page: 25, total_count: 0 },
          }),
      }),
    ),
  );

const renderAt = (path: string) =>
  renderWithClient(
    <MemoryRouter initialEntries={[path]}>
      <App />
    </MemoryRouter>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("renders the top bar, the navigation and the footer", async () => {
    serve();
    renderAt("/cores");

    expect(
      screen.getByRole("img", { name: "Ella IMS Logo" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("banner")).toHaveTextContent("Ella IMS");
    expect(
      await within(screen.getByRole("banner")).findByText("v0.0.1"),
    ).toBeInTheDocument();
    expect(
      within(screen.getByRole("navigation", { name: "Main" }))
        .getAllByRole("link")
        .map((link) => [link.textContent, link.getAttribute("href")]),
    ).toEqual([
      ["Cores", "/cores"],
      ["Operator", "/operator"],
      ["Registrations", "/registrations"],
      ["Calls", "/calls"],
    ]);
    expect(screen.getByRole("main")).toBeInTheDocument();
    expect(screen.getByRole("contentinfo")).toBeInTheDocument();

    const docs = screen.getByRole("link", { name: "Documentation" });
    expect(docs).toHaveAttribute("href", "https://docs.ellanetworks.com");
    expect(docs).toHaveAttribute("target", "_blank");

    const bug = screen.getByRole("link", { name: "Report a bug" });
    expect(bug).toHaveAttribute(
      "href",
      "https://github.com/ellanetworks/ims/issues/new/choose",
    );
    expect(bug).toHaveAttribute("target", "_blank");
  });

  it("opens the Cores page by default", async () => {
    serve();
    renderAt("/");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Cores" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Cores" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(document.title).toBe("Cores · Ella IMS");
  });

  it("navigates between pages", async () => {
    serve();
    renderAt("/cores");

    fireEvent.click(screen.getByRole("link", { name: "Operator" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Operator" }),
    ).toBeInTheDocument();
    expect(await screen.findByText("001 / 01")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("link", { name: "Registrations" }));
    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: "Registrations (0)",
      }),
    ).toBeInTheDocument();
  });
});
