import { useState } from "react";
import { Box, Skeleton } from "@mui/material";
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

export default function Profile() {
  const [changingPassword, setChangingPassword] = useState(false);
  const user = useQuery({ queryKey: ["users", "me"], queryFn: getMyUser });
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
          <UserAccountCard user={user.data} />
        ) : (
          <Skeleton variant="rounded" height={140} />
        )}
        <UserPasswordCard
          description="Changing your password ends your other sessions."
          onChangePassword={() => setChangingPassword(true)}
        />
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
