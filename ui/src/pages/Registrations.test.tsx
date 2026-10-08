import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Registrations from "@/pages/Registrations";
import type { Registration } from "@/queries/registrations";
import { contact, registration } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";

const alice = registration({
  contacts: [
    contact(),
    contact({
      contact: "sip:001010000000001@192.0.2.31:5060",
      instance: "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
      media: ["audio"],
      protected: false,
      signalling_path: "lost",
      expires_at: "2026-10-08T14:00:00.000Z",
    }),
  ],
});

const carol = registration({
  impi: "001010000000003@ims.mnc001.mcc001.3gppnetwork.org",
  identities: [{ uri: "tel:+15551230003", barred: false, registered_with: [] }],
  contacts: [
    contact({ contact: "sip:001010000000003@192.0.2.33:5064" }),
    contact({ contact: "sip:001010000000003@192.0.2.34:5064" }),
  ],
});

const bob = registration({
  impi: "001010000000002@ims.mnc001.mcc001.3gppnetwork.org",
  identities: [{ uri: "tel:+15551230002", barred: false, registered_with: [] }],
  contacts: [
    contact({
      contact: "sip:001010000000002@192.0.2.32:5064",
      instance: "urn:gsma:imei:35693803-564381-0",
      media: ["audio"],
      signalling_path: "unmonitored",
    }),
  ],
});

const serve = (
  items: Registration[],
  reauth: () => Response = () => json(202, { result: {} }),
) => {
  const urls: string[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(
      stubApi({
        "/api/v1/registrations": (url) => {
          urls.push(url.search);
          const search = url.searchParams.get("search") ?? "";
          const found = items.filter(
            (r) =>
              r.impi.includes(search) ||
              r.identities.some((i) => i.uri.includes(search)),
          );
          return json(200, {
            result: {
              items: found,
              page: Number(url.searchParams.get("page") ?? 1),
              per_page: Number(url.searchParams.get("per_page") ?? 25),
              total_count: found.length,
            },
          });
        },
        [`/api/v1/registrations/${encodeURIComponent(alice.impi)}/reauthenticate`]:
          reauth,
      }),
    ),
  );
  return urls;
};

const cells = (impi: string) =>
  within(screen.getByText(impi).closest('[role="row"]') as HTMLElement)
    .getAllByRole("gridcell")
    .map((cell) => cell.textContent);

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("Registrations", () => {
  it("lists the registrations", async () => {
    serve([alice, bob]);

    renderWithClient(<Registrations />);

    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: "Registrations (2)",
      }),
    ).toBeInTheDocument();
    await screen.findByText(alice.impi);
    expect(cells(alice.impi)).toEqual([
      alice.impi,
      "+15551230001",
      "35693803-564380-0urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6",
      "yes",
      "lost",
      "2026-10-08 14:00:00",
    ]);
    expect(cells(bob.impi)).toEqual([
      bob.impi,
      "+15551230002",
      "35693803-564381-0",
      "no",
      "—",
      "2026-10-08 13:00:00",
    ]);
  });

  it("lists a device with several contacts once", async () => {
    serve([carol]);

    renderWithClient(<Registrations />);

    await screen.findByText(carol.impi);
    expect(cells(carol.impi)[2]).toBe("35693803-564380-0");
  });

  it("shows when nothing is registered", async () => {
    serve([]);

    renderWithClient(<Registrations />);

    expect(await screen.findByText("No registrations.")).toBeInTheDocument();
  });

  it("searches", async () => {
    const urls = serve([alice, bob]);

    renderWithClient(<Registrations />);
    await screen.findByText(alice.impi);

    fireEvent.change(screen.getByRole("textbox", { name: "Search" }), {
      target: { value: " +15551230002 " },
    });

    await waitFor(() =>
      expect(screen.queryByText(alice.impi)).not.toBeInTheDocument(),
    );
    expect(screen.getByText(bob.impi)).toBeInTheDocument();
    expect(urls.at(-1)).toBe("?page=1&per_page=25&search=%2B15551230002");
  });

  it("shows the details of a registration", async () => {
    serve([alice]);

    renderWithClient(<Registrations />);
    fireEvent.click(await screen.findByText(alice.impi));

    const drawer = await screen.findByRole("dialog");
    expect(
      within(drawer).getByRole("heading", { name: alice.impi }),
    ).toBeInTheDocument();
    expect(
      within(drawer).getByRole("heading", { name: "Public Identities (3)" }),
    ).toBeInTheDocument();
    expect(within(drawer).getByText("barred")).toBeInTheDocument();
    expect(
      within(drawer).getByRole("heading", { name: "Contacts (2)" }),
    ).toBeInTheDocument();

    const [first] = within(drawer)
      .getAllByRole("listitem")
      .filter((li) => li.textContent?.startsWith("IMEI"));
    expect(first).toHaveTextContent(
      [
        "IMEI35693803-564380-0",
        "Address192.0.2.30:5064",
        "TransportUDP",
        "IPsecyes",
        "Mediaaudio, video",
        "Priority1.0",
        "Signalling Pathmonitored",
        "Registered2026-10-08 12:00:00",
        "Expires2026-10-08 13:00:00",
        "Contactsip:001010000000001@192.0.2.30:5064",
      ].join(""),
    );

    fireEvent.click(within(drawer).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("searches a number shared with other identities", async () => {
    const shared = registration({
      identities: [
        {
          uri: "tel:+15551230001",
          barred: false,
          registered_with: [bob.impi],
        },
      ],
      contacts: [contact({ q: 0.5 })],
    });
    const urls = serve([shared, bob]);

    renderWithClient(<Registrations />);
    fireEvent.click(await screen.findByText(shared.impi));

    const drawer = await screen.findByRole("dialog");
    expect(within(drawer).getByText("Priority")).toBeInTheDocument();
    expect(within(drawer).getByText("0.5")).toBeInTheDocument();

    expect(within(drawer).getByText("shared with 1 other")).toBeInTheDocument();
    fireEvent.click(
      within(drawer).getByRole("button", {
        name: `Search +15551230001: also registered with ${bob.impi}`,
      }),
    );

    await waitFor(() =>
      expect(urls.at(-1)).toBe("?page=1&per_page=25&search=%2B15551230001"),
    );
    expect(screen.getByRole("textbox", { name: "Search" })).toHaveValue(
      "+15551230001",
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("shows IPsec as unknown without the P-CSCF's flow", async () => {
    serve([
      registration({
        contacts: [
          contact({
            address: undefined,
            transport: undefined,
            protected: false,
          }),
        ],
      }),
    ]);

    renderWithClient(<Registrations />);
    fireEvent.click(await screen.findByText(alice.impi));

    const card = (await screen.findAllByRole("listitem")).find((li) =>
      li.textContent?.startsWith("IMEI"),
    );
    expect(card).toHaveTextContent("Address—Transport—IPsec—");
  });

  it("requests a re-authentication", async () => {
    serve([alice]);

    renderWithClient(<Registrations />);
    fireEvent.click(await screen.findByText(alice.impi));
    fireEvent.click(
      await screen.findByRole("button", { name: "Re-authenticate" }),
    );

    expect(await screen.findByText("requested")).toBeInTheDocument();
  });

  it("shows why a re-authentication failed", async () => {
    serve([alice], () =>
      json(404, { error: `no registration for ${alice.impi}` }),
    );

    renderWithClient(<Registrations />);
    fireEvent.click(await screen.findByText(alice.impi));
    fireEvent.click(
      await screen.findByRole("button", { name: "Re-authenticate" }),
    );

    expect(await screen.findByRole("alert")).toHaveTextContent(
      `Could not re-authenticate: no registration for ${alice.impi}`,
    );
  });
});
