import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { Box, CircularProgress } from "@mui/material";
import { useQueryClient } from "@tanstack/react-query";
import { logout as logoutRequest, refresh } from "@/queries/auth";
import { RoleID } from "@/queries/users";
import {
  ApiError,
  setAccessToken,
  setAccessTokenRenewer,
} from "@/queries/utils";
import { decodeAccessToken } from "@/utils/jwt";

export type Permission =
  | "operator:read"
  | "operator:update"
  | "diameter:read"
  | "diameter:update"
  | "policy:read"
  | "policy:update"
  | "sip:read"
  | "registration:read"
  | "registration:reauthenticate"
  | "call_record:read"
  | "call_record:read_retention"
  | "call_record:update_retention"
  | "ip_sm_gw:read"
  | "ip_sm_gw:update"
  | "mcptt:read"
  | "mcptt:update"
  | "user:admin"
  | "oidc:read"
  | "oidc:update";

const readPermissions: Permission[] = [
  "operator:read",
  "diameter:read",
  "policy:read",
  "sip:read",
  "registration:read",
  "call_record:read",
  "call_record:read_retention",
  "ip_sm_gw:read",
  "mcptt:read",
];

const operatePermissions: Permission[] = [
  "operator:update",
  "diameter:update",
  "policy:update",
  "registration:reauthenticate",
  "call_record:update_retention",
  "ip_sm_gw:update",
  "mcptt:update",
];

const permissions: Record<RoleID, Permission[]> = {
  [RoleID.Admin]: [
    ...readPermissions,
    ...operatePermissions,
    "user:admin",
    "oidc:read",
    "oidc:update",
  ],
  [RoleID.ReadOnly]: readPermissions,
  [RoleID.NetworkManager]: [...readPermissions, ...operatePermissions],
};

export interface Auth {
  email: string;
  roleId: RoleID;
  can: (permission: Permission) => boolean;
  logout: () => Promise<void>;
}

export const AuthContext = createContext<Auth | null>(null);

export const authFor = (
  email: string,
  roleId: RoleID,
  logout: () => Promise<void> = async () => {},
): Auth => ({
  email,
  roleId,
  can: (permission) => permissions[roleId]?.includes(permission) ?? false,
  logout,
});

const REFRESH_LEEWAY_MS = 120_000;
const MIN_REFRESH_DELAY_MS = 5_000;
const RETRY_DELAY_MS = 30_000;

export function AuthProvider({ children }: { children: ReactNode }) {
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const [auth, setAuth] = useState<Auth | null>(null);

  const navigateRef = useRef(navigate);
  const fromRef = useRef(location.pathname + location.search);
  useEffect(() => {
    navigateRef.current = navigate;
    fromRef.current = location.pathname + location.search;
  }, [navigate, location.pathname, location.search]);

  useEffect(() => {
    let expiresAt = 0;
    let timer: number | undefined;
    let inflight: Promise<boolean> | null = null;
    let over = false;

    const leave = (state?: { from: string }) => {
      over = true;
      window.clearTimeout(timer);
      setAccessToken(null);
      setAuth(null);
      queryClient.clear();
      navigateRef.current("/login", { replace: true, state });
    };

    const logout = () =>
      logoutRequest()
        .catch(() => {})
        .finally(() => leave());

    const schedule = (delay: number) => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => void renew(), delay);
    };

    const renew = (): Promise<boolean> => {
      if (over) return Promise.resolve(false);
      inflight ??= (async () => {
        try {
          const { token } = await refresh();
          if (over) return false;
          const claims = decodeAccessToken(token);
          if (!claims) {
            leave();
            return false;
          }
          setAccessToken(token);
          expiresAt = claims.exp * 1000;
          setAuth(authFor(claims.email, claims.role_id, logout));
          schedule(
            Math.max(
              MIN_REFRESH_DELAY_MS,
              expiresAt - Date.now() - REFRESH_LEEWAY_MS,
            ),
          );
          return true;
        } catch (err) {
          if (over) return false;
          if (err instanceof ApiError && err.status === 401) {
            leave({ from: fromRef.current });
          } else {
            schedule(RETRY_DELAY_MS);
          }
          return false;
        } finally {
          inflight = null;
        }
      })();
      return inflight;
    };

    const onVisible = () => {
      if (
        document.visibilityState === "visible" &&
        expiresAt - Date.now() < REFRESH_LEEWAY_MS
      ) {
        void renew();
      }
    };

    void renew();
    document.addEventListener("visibilitychange", onVisible);
    setAccessTokenRenewer(renew);

    return () => {
      over = true;
      window.clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
      setAccessTokenRenewer(null);
    };
    // oxlint-disable-next-line react/exhaustive-effect-dependencies
  }, [queryClient]);

  if (!auth) {
    return (
      <Box
        sx={{
          display: "flex",
          justifyContent: "center",
          alignItems: "center",
          height: "100vh",
        }}
      >
        <CircularProgress aria-label="Loading" />
      </Box>
    );
  }

  return <AuthContext.Provider value={auth}>{children}</AuthContext.Provider>;
}

export function useAuth(): Auth {
  const auth = useContext(AuthContext);
  if (!auth) throw new Error("useAuth outside of an AuthProvider");
  return auth;
}
