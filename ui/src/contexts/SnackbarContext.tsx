import {
  createContext,
  useCallback,
  useContext,
  useState,
  type ReactNode,
  type SyntheticEvent,
} from "react";
import { Alert, Button, Snackbar, type AlertColor } from "@mui/material";

// SnackbarAction is a button in a notification, such as View on a sent message.
export interface SnackbarAction {
  label: string;
  onClick: () => void;
}

interface SnackbarContextValue {
  showSnackbar: (
    message: string,
    severity: AlertColor,
    action?: SnackbarAction,
  ) => void;
}

const SnackbarContext = createContext<SnackbarContextValue | undefined>(
  undefined,
);

export const AUTO_HIDE_MS = 5000;

// SnackbarProvider shows the outcome of an action, as Ella Core does: a filled alert at the top of the page, which
// hides itself.
export function SnackbarProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [message, setMessage] = useState("");
  const [severity, setSeverity] = useState<AlertColor>("success");
  const [action, setAction] = useState<SnackbarAction>();
  const [key, setKey] = useState(0);

  const showSnackbar = useCallback(
    (msg: string, sev: AlertColor, act?: SnackbarAction) => {
      setMessage(msg);
      setSeverity(sev);
      setAction(act);
      setOpen(true);
      setKey((prev) => prev + 1);
    },
    [],
  );

  const handleClose = (
    _event: SyntheticEvent | Event | null,
    reason?: string,
  ) => {
    if (reason === "clickaway") return;
    setOpen(false);
  };

  return (
    <SnackbarContext.Provider value={{ showSnackbar }}>
      {children}
      <Snackbar
        key={key}
        open={open}
        autoHideDuration={AUTO_HIDE_MS}
        onClose={handleClose}
        anchorOrigin={{ vertical: "top", horizontal: "center" }}
      >
        <Alert
          onClose={() => handleClose(null)}
          severity={severity}
          variant="filled"
          sx={{ width: "100%" }}
          action={
            action && (
              <Button
                color="inherit"
                size="small"
                onClick={() => {
                  setOpen(false);
                  action.onClick();
                }}
              >
                {action.label}
              </Button>
            )
          }
        >
          {message}
        </Alert>
      </Snackbar>
    </SnackbarContext.Provider>
  );
}

export function useSnackbar(): SnackbarContextValue {
  const ctx = useContext(SnackbarContext);
  if (!ctx) {
    throw new Error("useSnackbar must be used within a SnackbarProvider");
  }
  return ctx;
}

// errorMessage is the message of a failure, for a notification.
export const errorMessage = (error: unknown): string =>
  error instanceof Error ? error.message : "Unknown error occurred.";
