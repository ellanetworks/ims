import { useState } from "react";
import { Box, Button, Skeleton } from "@mui/material";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";
import ConfirmDialog from "@/components/ConfirmDialog";
import PageHeader from "@/components/PageHeader";
import QueryAlert from "@/components/QueryAlert";
import {
  tokenQueryKey,
  UserAccountCard,
  UserAPITokensCard,
  UserPasswordCard,
} from "@/components/UserCards";
import { EditRoleDialog, PasswordDialog } from "@/components/UserDialogs";
import { useAuth } from "@/contexts/AuthContext";
import { errorMessage, useSnackbar } from "@/contexts/SnackbarContext";
import { listAPITokens } from "@/queries/api_tokens";
import { getOIDC, roleFromGroups } from "@/queries/oidc";
import {
  deleteUser,
  getUser,
  removeUserPassword,
  unlinkUser,
} from "@/queries/users";

const removePasswordDescription = (
  email: string,
  ssoEnabled: boolean,
  linked: boolean,
) => {
  const consequence = !ssoEnabled
    ? "Single sign-on is off, so they cannot sign in until it is on."
    : linked
      ? "They can then sign in only with single sign-on."
      : "They have not signed in with single sign-on yet.";
  return `Remove the password of ${email}? ${consequence} Their sessions end.`;
};

export default function UserDetail() {
  const email = useParams<{ email: string }>().email ?? "";
  const auth = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [editing, setEditing] = useState<
    "role" | "password" | "delete" | "removePassword" | "unlink" | null
  >(null);

  const self = email.toLowerCase() === auth.email.toLowerCase();

  const user = useQuery({
    queryKey: ["users", "user", email],
    queryFn: () => getUser(email),
  });
  const oidc = useQuery({ queryKey: ["oidc"], queryFn: getOIDC });
  const tokens = useQuery({
    queryKey: tokenQueryKey(email),
    queryFn: () => listAPITokens(email),
  });

  const remove = useMutation({
    mutationFn: () => deleteUser(email),
    onSuccess: () => {
      queryClient.removeQueries({ queryKey: ["users", "user", email] });
      queryClient.removeQueries({ queryKey: tokenQueryKey(email) });
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      showSnackbar("User deleted successfully.", "success");
      navigate("/users", { replace: true });
    },
    onError: (error) => {
      setEditing(null);
      showSnackbar(`Failed to delete user: ${errorMessage(error)}`, "error");
    },
  });

  const change = (request: () => Promise<void>, done: string, failed: string) =>
    ({
      mutationFn: request,
      onSuccess: () => {
        setEditing(null);
        void queryClient.invalidateQueries({ queryKey: ["users"] });
        showSnackbar(done, "success");
      },
      onError: (error: Error) => {
        setEditing(null);
        showSnackbar(`${failed}: ${errorMessage(error)}`, "error");
      },
    }) as const;

  const removePassword = useMutation(
    change(
      () => removeUserPassword(email),
      "Password removed successfully.",
      "Failed to remove the password",
    ),
  );
  const unlink = useMutation(
    change(
      () => unlinkUser(email),
      "User unlinked successfully.",
      "Failed to unlink the user",
    ),
  );

  return (
    <Box component="section" aria-labelledby="user-title">
      <PageHeader
        id="user-title"
        title={email}
        parent={{ label: "Users", to: "/users" }}
        action={
          !self && (
            <Button
              variant="outlined"
              color="error"
              onClick={() => setEditing("delete")}
            >
              Delete
            </Button>
          )
        }
      />
      <QueryAlert query={user} subject="this user" />
      <QueryAlert query={tokens} subject="this user's API tokens" />
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
            fromGroups={roleFromGroups(user.data, oidc.data)}
            onEditRole={self ? undefined : () => setEditing("role")}
            onUnlink={self ? undefined : () => setEditing("unlink")}
          />
        ) : (
          <Skeleton variant="rounded" height={140} />
        )}
        <UserPasswordCard
          description={
            user.data?.password_set === false
              ? "This user has no password and signs in only with single sign-on."
              : "Setting a new password ends the user's sessions. Its API tokens keep working."
          }
          changeLabel={
            user.data?.password_set === false
              ? "Set Password"
              : "Change Password"
          }
          onChangePassword={() => setEditing("password")}
          onRemovePassword={
            user.data?.password_set
              ? () => setEditing("removePassword")
              : undefined
          }
          disabledReason={
            self ? "Change your own password on your profile." : undefined
          }
        />
      </Box>
      <Box sx={{ mt: 3 }}>
        {tokens.data ? (
          <UserAPITokensCard tokens={tokens.data.items} email={email} />
        ) : (
          <Skeleton variant="rounded" height={240} />
        )}
      </Box>
      {editing === "role" && user.data && (
        <EditRoleDialog user={user.data} onClose={() => setEditing(null)} />
      )}
      {editing === "password" && (
        <PasswordDialog email={email} onClose={() => setEditing(null)} />
      )}
      {editing === "removePassword" && (
        <ConfirmDialog
          title="Remove Password"
          description={removePasswordDescription(
            email,
            oidc.data?.enabled === true,
            user.data?.oidc_linked === true,
          )}
          confirmLabel="Remove"
          pending={removePassword.isPending}
          onConfirm={() => removePassword.mutate()}
          onClose={() => setEditing(null)}
        />
      )}
      {editing === "unlink" && (
        <ConfirmDialog
          title="Unlink from Identity Provider"
          description={`Unlink ${email} from their identity provider account? Their single sign-on sessions end, and their next sign-in links them again.`}
          confirmLabel="Unlink"
          pending={unlink.isPending}
          onConfirm={() => unlink.mutate()}
          onClose={() => setEditing(null)}
        />
      )}
      {editing === "delete" && (
        <ConfirmDialog
          title="Delete User"
          description={`Delete ${email}? Its API tokens stop working. This cannot be undone.`}
          confirmLabel="Delete"
          pending={remove.isPending}
          onConfirm={() => remove.mutate()}
          onClose={() => setEditing(null)}
        />
      )}
    </Box>
  );
}
