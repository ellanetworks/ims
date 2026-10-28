import { useState } from "react";
import { Box, Button, DialogContentText, Skeleton } from "@mui/material";
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
import { useSnackbar } from "@/contexts/SnackbarContext";
import { listAPITokens } from "@/queries/api_tokens";
import { deleteUser, getUser } from "@/queries/users";

export default function UserDetail() {
  const email = useParams<{ email: string }>().email ?? "";
  const auth = useAuth();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [editing, setEditing] = useState<"role" | "password" | "delete" | null>(
    null,
  );

  // An admin manages its own account on its profile: the server refuses it here.
  const self = email.toLowerCase() === auth.email.toLowerCase();

  const user = useQuery({
    queryKey: ["users", "user", email],
    queryFn: () => getUser(email),
  });
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
  });

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
              onClick={() => {
                remove.reset();
                setEditing("delete");
              }}
            >
              Delete
            </Button>
          )
        }
      />
      <QueryAlert
        error={user.error}
        hasData={user.data !== undefined}
        subject="this user"
      />
      <QueryAlert
        error={tokens.error}
        hasData={tokens.data !== undefined}
        subject="this user's API tokens"
      />
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
            onEditRole={self ? undefined : () => setEditing("role")}
          />
        ) : (
          <Skeleton variant="rounded" height={140} />
        )}
        <UserPasswordCard
          description="Setting a new password ends the user's sessions. Its API tokens keep working."
          onChangePassword={() => setEditing("password")}
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
      {editing === "delete" && (
        <ConfirmDialog
          title={`Delete ${email}?`}
          action="Delete"
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() => remove.mutate()}
          onClose={() => setEditing(null)}
        >
          <DialogContentText>
            Its API tokens stop working. This cannot be undone.
          </DialogContentText>
        </ConfirmDialog>
      )}
    </Box>
  );
}
