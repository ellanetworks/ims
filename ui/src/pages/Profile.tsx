import { useEffect, useState } from "react";
import { useLocation, useSearchParams } from "react-router-dom";
import { Alert, Box, Skeleton } from "@mui/material";
import { useQuery } from "@tanstack/react-query";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import {
  tokenQueryKey,
  UserAccountCard,
  UserAPITokensCard,
  UserPasswordCard,
} from "@/components/UserCards";
import { PasswordDialog } from "@/components/UserDialogs";
import { listAPITokens } from "@/queries/api_tokens";
import { getMyUser } from "@/queries/users";
import {
  forgetLinking,
  getOIDCLogin,
  isLinking,
  OIDC_LINK_PATH,
  oidcLinkError,
  rememberLinking,
} from "@/queries/oidc";

type LinkResult = { error: string } | { linked: true } | null;

export default function Profile() {
  const location = useLocation();
  const [params, setParams] = useSearchParams();
  const [linkResult, setLinkResult] = useState<LinkResult>(() => {
    const error = params.get("error");
    if (error) return { error };
    return isLinking() ? { linked: true } : null;
  });

  useEffect(() => {
    forgetLinking();
    if (params.has("error")) {
      setParams(
        (current) => {
          const next = new URLSearchParams(current);
          next.delete("error");
          return next;
        },
        { replace: true, state: location.state },
      );
    }
  }, [params, setParams, location.state]);

  const [changingPassword, setChangingPassword] = useState(false);
  const user = useQuery({ queryKey: ["users", "me"], queryFn: getMyUser });
  const oidcLogin = useQuery({
    queryKey: ["oidcLogin"],
    queryFn: getOIDCLogin,
    retry: false,
  });
  const ssoName = oidcLogin.data?.enabled
    ? oidcLogin.data.display_name
    : undefined;
  const tokens = useQuery({
    queryKey: tokenQueryKey(),
    queryFn: () => listAPITokens(),
  });

  return (
    <Box component="section" aria-labelledby="profile-title">
      <PageHeader
        id="profile-title"
        title="Profile"
        description="Your account, your password, and your API tokens."
      />
      {linkResult && "error" in linkResult && (
        <Alert
          severity="error"
          onClose={() => setLinkResult(null)}
          sx={{ mb: 3 }}
        >
          {oidcLinkError(linkResult.error)}
        </Alert>
      )}
      {linkResult && "linked" in linkResult && user.data?.oidc_linked && (
        <Alert
          severity="success"
          onClose={() => setLinkResult(null)}
          sx={{ mb: 3 }}
        >
          Your account is linked. You can now log in with{" "}
          {ssoName ?? "single sign-on"}.
        </Alert>
      )}
      <QueryAlert query={user} subject="your account" />
      <QueryAlert query={tokens} subject="your API tokens" />
      <Box
        sx={{
          display: "grid",
          gridTemplateColumns: { xs: "1fr", md: "1fr 1fr" },
          gap: 3,
        }}
      >
        {user.data ? (
          <UserAccountCard
            user={user.data}
            link={
              ssoName
                ? {
                    label: `Link ${ssoName} account`,
                    href: OIDC_LINK_PATH,
                    onClick: rememberLinking,
                  }
                : undefined
            }
          />
        ) : (
          <Skeleton variant="rounded" height={140} />
        )}
        {user.data?.password_set === false ? (
          <UserPasswordCard description="You sign in with single sign-on, and have no password." />
        ) : (
          <UserPasswordCard
            description="Changing your password ends your other sessions."
            onChangePassword={() => setChangingPassword(true)}
          />
        )}
      </Box>
      <Box sx={{ mt: 3 }}>
        {tokens.data ? (
          <UserAPITokensCard tokens={tokens.data.items} />
        ) : (
          <Skeleton variant="rounded" height={240} />
        )}
      </Box>
      {changingPassword && (
        <PasswordDialog onClose={() => setChangingPassword(false)} />
      )}
    </Box>
  );
}
