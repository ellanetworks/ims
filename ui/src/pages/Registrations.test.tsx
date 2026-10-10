import { afterEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import Registrations from "@/pages/Registrations";
import type { Registration } from "@/queries/registrations";
import { contact, registration } from "@/test/fixtures";
import { json, renderWithClient, stubApi } from "@/test/render";
import { identitiesOf } from "@/utils/registrations";

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
              identitiesOf(r).some((i) => i.uri.includes(search)),
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

// imsiOf is how the table shows a registration: the IMSI of its IMPI.
const imsiOf = (r: Registration) => r.impi.split("@")[0];

const rowOf = (r: Registration) =>
  screen.getByText(imsiOf(r)).closest('[role="row"]') as HTMLElement;

const cells = (r: Registration) =>
  within(rowOf(r))
    .getAllByRole("gridcell")
    .map((cell) => cell.textContent);

const open = async (r: Registration) => {
  fireEvent.click(await screen.findByText(imsiOf(r)));
  return screen.findByRole("dialog");
};

// fields is the keys and values of a section of the drawer.
const fields = (drawer: HTMLElement, section: string) =>
  within(within(drawer).getByRole("region", { name: section }))
    .getAllByRole("term")
    .map((dt) => [dt.textContent, dt.nextElementSibling?.textContent]);

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
    await screen.findByText(imsiOf(alice));
    expect(cells(alice)).toEqual([
      "+15551230001",
      "001010000000001",
      "2lost",
      "",
    ]);
    expect(within(rowOf(alice)).getByLabelText("voice")).toBeInTheDocument();
    expect(within(rowOf(alice)).getByLabelText("video")).toBeInTheDocument();
    expect(cells(bob)).toEqual(["+15551230002", "001010000000002", "1", ""]);
    expect(within(rowOf(bob)).getByLabelText("voice")).toBeInTheDocument();
    expect(
      within(rowOf(bob)).queryByLabelText("video"),
    ).not.toBeInTheDocument();
  });

  it("counts a device with several contacts once", async () => {
    serve([carol]);

    renderWithClient(<Registrations />);

    await screen.findByText(imsiOf(carol));
    expect(cells(carol)[2]).toBe("1");
  });

  it("shows an IMPI not derived from an IMSI whole", async () => {
    serve([registration({ impi: "alice@ims.example.org" })]);

    renderWithClient(<Registrations />);

    expect(
      await screen.findByText("alice@ims.example.org"),
    ).toBeInTheDocument();
  });

  it("shows when nothing is registered", async () => {
    serve([]);

    renderWithClient(<Registrations />);

    expect(await screen.findByText("No registrations.")).toBeInTheDocument();
  });

  it("searches", async () => {
    const urls = serve([alice, bob]);

    renderWithClient(<Registrations />);
    await screen.findByText(imsiOf(alice));

    fireEvent.change(screen.getByRole("textbox", { name: "Search" }), {
      target: { value: " +15551230002 " },
    });

    await waitFor(() =>
      expect(screen.queryByText(imsiOf(alice))).not.toBeInTheDocument(),
    );
    expect(screen.getByText(imsiOf(bob))).toBeInTheDocument();
    expect(urls.at(-1)).toBe("?page=1&per_page=25&search=%2B15551230002");
  });

  it("shows the details of a registration", async () => {
    serve([alice]);

    renderWithClient(<Registrations />);
    const drawer = await open(alice);

    expect(
      within(drawer).getByRole("heading", { name: "+15551230001" }),
    ).toBeInTheDocument();
    expect(fields(drawer, "Subscriber")).toEqual([
      ["Number", "+15551230001"],
      ["IMSI", "001010000000001"],
      ["HSS", "mmec01.mmegi0001.mme.epc.mnc001.mcc001.3gppnetwork.org"],
    ]);
    expect(fields(drawer, "Device 1")).toEqual([
      ["IMEI", "35693803-564380-0"],
      ["Address", "192.0.2.30:5064"],
      ["Transport", "UDP"],
      ["IPsec", "Yes"],
      ["Media", "Voice, Video"],
      ["Signalling", "Monitored"],
      ["Registered", "2026-10-08 12:00:00"],
      ["Expires", "2026-10-08 13:00:00"],
    ]);
    expect(fields(drawer, "Device 2")).toEqual([
      ["Instance", "urn:uuid:f81d4fae-7dec-11d0-a765-00a0c91e6bf6"],
      ["Address", "192.0.2.30:5064"],
      ["Transport", "UDP"],
      ["IPsec", "No"],
      ["Media", "Voice"],
      ["Signalling", "Lost"],
      ["Registered", "2026-10-08 12:00:00"],
      ["Expires", "2026-10-08 14:00:00"],
    ]);
    // The barred IMPU is the device's own, for registering: it is not shown.
    expect(within(drawer).queryByText(/^sip:/)).not.toBeInTheDocument();

    fireEvent.click(within(drawer).getByRole("button", { name: "Close" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  // RFC 5626: a device with several registration flows.
  it("shows the flows of a device", async () => {
    const flows = registration({
      contacts: [
        contact({ reg_id: 1 }),
        contact({ reg_id: 2, address: "192.0.2.40:5064" }),
      ],
    });
    serve([flows]);

    renderWithClient(<Registrations />);
    await screen.findByText(imsiOf(flows));
    expect(cells(flows)[2]).toBe("1");

    const drawer = await open(flows);
    expect(fields(drawer, "Device 1 Flow 1")).toContainEqual([
      "Address",
      "192.0.2.30:5064",
    ]);
    expect(fields(drawer, "Device 1 Flow 2")).toContainEqual([
      "Address",
      "192.0.2.40:5064",
    ]);
    expect(within(drawer).queryByText("Device 2")).not.toBeInTheDocument();
  });

  it("merges the implicit registration sets", async () => {
    const twoSets = registration({
      implicit_registration_sets: [
        {
          hss: { host: "hss1.example.org", realm: "example.org" },
          identities: [
            { uri: "tel:+15551230001", barred: false, registered_with: [] },
          ],
          contacts: [contact()],
        },
        {
          hss: { host: "hss1.example.org", realm: "example.org" },
          identities: [
            { uri: "tel:+15551230009", barred: false, registered_with: [] },
          ],
          contacts: [contact()],
        },
      ],
    });
    serve([twoSets]);

    renderWithClient(<Registrations />);
    const drawer = await open(twoSets);

    expect(fields(drawer, "Subscriber")).toEqual([
      ["Number", "+15551230001, +15551230009"],
      ["IMSI", "001010000000001"],
      ["HSS", "hss1.example.org"],
    ]);
    expect(within(drawer).queryByText("Device 2")).not.toBeInTheDocument();
  });

  it("links to the other private identities on a number", async () => {
    const shared = registration({
      identities: [
        {
          uri: "tel:+15551230001",
          barred: false,
          registered_with: [bob.impi],
        },
      ],
    });
    const urls = serve([shared, bob]);

    renderWithClient(<Registrations />);
    await screen.findByText(imsiOf(shared));
    expect(cells(shared)[0]).toBe("+15551230001");
    expect(cells(bob)[0]).toBe("+15551230002");

    const drawer = await open(shared);
    expect(fields(drawer, "Subscriber")).toContainEqual([
      "Also on",
      "IMSI 001010000000002",
    ]);
    fireEvent.click(
      within(drawer).getByRole("button", { name: "IMSI 001010000000002" }),
    );

    await waitFor(() =>
      expect(urls.at(-1)).toBe("?page=1&per_page=25&search=001010000000002"),
    );
    expect(screen.getByRole("textbox", { name: "Search" })).toHaveValue(
      "001010000000002",
    );
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
  });

  it("does not claim IPsec without the P-CSCF's flow", async () => {
    const unknown = registration({
      contacts: [
        contact({
          address: undefined,
          transport: undefined,
          protected: false,
        }),
      ],
    });
    serve([unknown]);

    renderWithClient(<Registrations />);
    const drawer = await open(unknown);

    expect(fields(drawer, "Device 1").slice(1, 4)).toEqual([
      ["Address", "—"],
      ["Transport", "—"],
      ["IPsec", "—"],
    ]);
  });

  it("shows only the media a device declared", async () => {
    const silent = registration({ contacts: [contact({ media: [] })] });
    serve([silent]);

    renderWithClient(<Registrations />);
    await screen.findByText(imsiOf(silent));
    expect(
      within(rowOf(silent)).queryByLabelText("voice"),
    ).not.toBeInTheDocument();

    const drawer = await open(silent);
    expect(fields(drawer, "Device 1")).toContainEqual(["Media", "—"]);
  });

  it("re-authenticates from the list", async () => {
    const reauth = vi.fn(() => json(202, { result: {} }));
    serve([alice], reauth);

    renderWithClient(<Registrations />);
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Re-authenticate 001010000000001",
      }),
    );

    const dialog = screen.getByRole("dialog", {
      name: "Re-authenticate +15551230001?",
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Re-authenticate" }),
    );

    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    );
    expect(reauth).toHaveBeenCalledTimes(1);
  });

  it("shows why a re-authentication failed", async () => {
    serve([alice], () =>
      json(404, { error: `no registration for ${alice.impi}` }),
    );

    renderWithClient(<Registrations />);
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Re-authenticate 001010000000001",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Re-authenticate" }));

    expect(await screen.findByRole("alert")).toHaveTextContent(
      `Could not re-authenticate: no registration for ${alice.impi}`,
    );
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
