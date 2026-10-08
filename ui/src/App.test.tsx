import { afterEach, describe, expect, it, vi } from "vitest";
import { screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import App from "@/App";
import { json, renderWithClient, stubApi } from "@/test/render";

const serve = () =>
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/status": () =>
          json(200, { result: { version: "v0.0.1", revision: "" } }),
        "/api/v1/operator": () =>
          json(200, {
            result: {
              mcc: "001",
              mnc: "01",
              numbering: {
                country_code: "1",
                national_prefix: "",
                international_prefix: "",
              },
            },
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
    renderAt("/");

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
    ).toEqual([["Operator", "/operator"]]);
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

  it("opens the Operator page by default", async () => {
    serve();
    renderAt("/");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Operator" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Operator" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(document.title).toBe("Operator · Ella IMS");
  });
});
