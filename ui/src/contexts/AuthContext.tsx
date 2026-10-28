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

// Permission is what the server checks on a route. The UI only hides what a role cannot do; the server enforces it.
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
  | "user:admin";

// readPermissions are reading every setting and every state of the IMS.
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

// operatePermissions are changing the settings of the IMS, and acting on its registrations.
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
  [RoleID.Admin]: [...readPermissions, ...operatePermissions, "user:admin"],
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

// Refresh the access token this long before it expires, no sooner than MIN_REFRESH_DELAY_MS, and RETRY_DELAY_MS after
// a refresh that failed without ending the session.
const REFRESH_LEEWAY_MS = 120_000;
const MIN_REFRESH_DELAY_MS = 5_000;
const RETRY_DELAY_MS = 30_000;

// AuthProvider renders its children once it has an access token from the session cookie, which it refreshes before
// it expires, when the tab becomes visible again, and when the server refuses it. When the session is over, it goes
// to the login page.
export function AuthProvider({ children }: { children: ReactNode }) {
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const [auth, setAuth] = useState<Auth | null>(null);

  // navigate changes with the location: the session reads both through refs, so that it starts once.
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

    // renew resolves to whether there is a new access token. Only a refused refresh ends the session: when the
    // server is unreachable or failing, the current token stays until the next attempt.
    const renew = (): Promise<boolean> => {
      if (over) return Promise.resolve(false);
      inflight ??= (async () => {
        try {
          const { token } = await refresh();
          // A session stopped meanwhile, as React's StrictMode does once, leaves the state alone.
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
    // The query client is stable, so the session starts once. react-hooks requires it, and this rule calls it extra.
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
