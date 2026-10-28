import { describe, expect, it } from "vitest";
import { RoleID } from "@/queries/users";
import { makeToken } from "@/test/jwt";
import { decodeAccessToken } from "@/utils/jwt";

describe("decodeAccessToken", () => {
  it("reads the claims", () => {
    expect(
      decodeAccessToken(makeToken("nm@example.com", RoleID.NetworkManager, 60)),
    ).toMatchObject({
      email: "nm@example.com",
      role_id: RoleID.NetworkManager,
    });
  });

  it.each([
    ["no payload", "header"],
    ["not base64", "a.!!!.c"],
    ["not JSON", `a.${btoa("nope")}.c`],
    ["missing claims", `a.${btoa(JSON.stringify({ email: "a@b" }))}.c`],
  ])("rejects a token with %s", (_name, token) => {
    expect(decodeAccessToken(token)).toBeNull();
  });
});
