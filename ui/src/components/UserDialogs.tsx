import { useState } from "react";
import { TextField } from "@mui/material";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import EditDialog from "@/components/EditDialog";
import { useSnackbar } from "@/contexts/SnackbarContext";
import PasswordField from "@/components/PasswordField";
import RoleField from "@/components/RoleField";
import {
  createUser,
  passwordError,
  RoleID,
  updateMyPassword,
  updateUserPassword,
  updateUserRole,
  type User,
} from "@/queries/users";

const EMAIL = /^[^\s@]+@[^\s@]+$/;

export function CreateUserDialog({ onClose }: { onClose: () => void }) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState(RoleID.ReadOnly);

  const mutation = useMutation({
    mutationFn: () => createUser(email, password, role),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      showSnackbar("User created successfully.", "success");
      onClose();
    },
  });

  const emailError =
    email === "" || EMAIL.test(email) ? undefined : "Enter an email address.";
  const newPasswordError =
    password === "" ? undefined : passwordError(password);

  return (
    <EditDialog
      title="Create User"
      valid={EMAIL.test(email) && passwordError(password) === undefined}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
      submitLabel="Create"
      pendingLabel="Creating…"
    >
      <TextField
        label="Email"
        type="email"
        value={email}
        onChange={(e) => setEmail(e.target.value.trim())}
        error={emailError !== undefined}
        helperText={emailError}
        required
        autoFocus
      />
      <PasswordField
        label="Password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={newPasswordError !== undefined}
        helperText={newPasswordError ?? "8 to 72 characters."}
        required
        autoComplete="new-password"
      />
      <RoleField value={role} onChange={setRole} />
    </EditDialog>
  );
}

export function EditRoleDialog({
  user,
  onClose,
}: {
  user: User;
  onClose: () => void;
}) {
  const queryClient = useQueryClient();
  const { showSnackbar } = useSnackbar();
  const [role, setRole] = useState(user.role_id);

  const mutation = useMutation({
    mutationFn: () => updateUserRole(user.email, role),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["users"] });
      showSnackbar("User updated successfully.", "success");
      onClose();
    },
  });

  return (
    <EditDialog
      title="Edit Role"
      valid={role !== user.role_id}
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
    >
      <TextField label="Email" value={user.email} disabled />
      <RoleField value={role} onChange={setRole} />
    </EditDialog>
  );
}

// PasswordDialog changes the password of the logged-in user when email is undefined, with its current password, and
// otherwise sets the password of that user.
export function PasswordDialog({
  email,
  onClose,
}: {
  email?: string;
  onClose: () => void;
}) {
  const { showSnackbar } = useSnackbar();
  const [current, setCurrent] = useState("");
  const [password, setPassword] = useState("");

  const mutation = useMutation({
    mutationFn: () =>
      email === undefined
        ? updateMyPassword(current, password)
        : updateUserPassword(email, password),
    onSuccess: () => {
      showSnackbar("Password updated successfully.", "success");
      onClose();
    },
  });

  const newPasswordError =
    password === "" ? undefined : passwordError(password);

  return (
    <EditDialog
      title="Change Password"
      valid={
        (email !== undefined || current !== "") &&
        passwordError(password) === undefined
      }
      pending={mutation.isPending}
      error={mutation.error}
      onSubmit={() => mutation.mutate()}
      onClose={onClose}
    >
      {email === undefined && (
        <PasswordField
          label="Current password"
          value={current}
          onChange={(e) => setCurrent(e.target.value)}
          required
          autoFocus
          autoComplete="current-password"
        />
      )}
      <PasswordField
        label="New password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        error={newPasswordError !== undefined}
        helperText={
          newPasswordError ??
          (email === undefined
            ? "8 to 72 characters. Your other sessions end."
            : "8 to 72 characters. The user's sessions end.")
        }
        required
        autoFocus={email !== undefined}
        autoComplete="new-password"
      />
    </EditDialog>
  );
}
