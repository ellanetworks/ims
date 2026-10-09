import { useEffect, useRef, useState, type ReactNode } from "react";
import {
  AppBar,
  Box,
  Divider,
  Drawer,
  IconButton,
  List,
  ListItem,
  ListItemButton,
  ListItemIcon,
  ListItemText,
  ListSubheader,
  Toolbar,
  Typography,
  useMediaQuery,
} from "@mui/material";
import { useTheme } from "@mui/material/styles";
import {
  BugReport as BugReportIcon,
  Call as CallIcon,
  Feed as FeedIcon,
  Hub as HubIcon,
  Info as InfoIcon,
  Menu as MenuIcon,
  PhoneAndroid as PhoneAndroidIcon,
  OpenInNew as OpenInNewIcon,
} from "@mui/icons-material";
import { Link, useLocation } from "react-router-dom";
import DeploymentIdentity from "@/components/DeploymentIdentity";
import Footer from "@/components/Footer";
import Logo from "@/components/Logo";
import { MAX_WIDTH, PAGE_PADDING_X } from "@/utils/layout";

const drawerWidth = 250;

const DOCS_URL = "https://docs.ellanetworks.com";

const NEW_ISSUE_URL = "https://github.com/ellanetworks/ims/issues/new/choose";

const drawerSelectedSx = {
  "& .MuiListItemText-primary": { color: "primary.main" },

  "&:hover": { bgcolor: "transparent" },
  "&.Mui-selected": { bgcolor: "transparent" },
  "&.Mui-selected:hover": { bgcolor: "transparent" },

  "&.Mui-selected .MuiListItemText-primary": {
    fontWeight: 700,
    textDecoration: "underline",
    textDecorationColor: "primary.main",
    textUnderlineOffset: "4px",
    textDecorationThickness: "2px",
  },

  "&:hover .MuiListItemText-primary": {
    textDecoration: "underline",
    textDecorationColor: "primary.main",
    textUnderlineOffset: "4px",
    textDecorationThickness: "2px",
  },
};

const navItems = [
  { to: "/cores", label: "Cores", icon: <HubIcon color="primary" /> },
  {
    to: "/operator",
    label: "Operator",
    icon: <FeedIcon color="primary" />,
  },
  {
    to: "/registrations",
    label: "Registrations",
    icon: <PhoneAndroidIcon color="primary" />,
  },
  { to: "/calls", label: "Calls", icon: <CallIcon color="primary" /> },
];

export default function Layout({ children }: { children: ReactNode }) {
  const { pathname } = useLocation();
  const theme = useTheme();
  const isMobile = useMediaQuery(theme.breakpoints.down("lg"));
  const [mobileOpen, setMobileOpen] = useState(false);

  const isFirstRender = useRef(true);
  useEffect(() => {
    if (isFirstRender.current) {
      isFirstRender.current = false;
      return;
    }
    document.getElementById("main-content")?.focus();
    // oxlint-disable-next-line react/exhaustive-effect-dependencies
  }, [pathname]);

  const handleNavClick = () => {
    if (isMobile) setMobileOpen(false);
  };

  return (
    <Box sx={{ display: "flex" }}>
      <Box
        component="a"
        href="#main-content"
        onClick={(e: React.MouseEvent<HTMLAnchorElement>) => {
          e.preventDefault();
          document.getElementById("main-content")?.focus();
        }}
        sx={{
          position: "absolute",
          left: "-9999px",
          top: "auto",
          width: "1px",
          height: "1px",
          overflow: "hidden",
          zIndex: (t) => t.zIndex.modal + 1,
          "&:focus": {
            position: "fixed",
            top: 8,
            left: 8,
            width: "auto",
            height: "auto",
            overflow: "visible",
            bgcolor: "background.paper",
            color: "primary.main",
            px: 2,
            py: 1,
            borderRadius: 1,
            boxShadow: 3,
            fontWeight: 700,
            textDecoration: "none",
          },
        }}
      >
        Skip to main content
      </Box>
      <AppBar position="fixed" sx={{ zIndex: (t) => t.zIndex.drawer + 1 }}>
        <Toolbar>
          {isMobile && (
            <IconButton
              color="inherit"
              aria-label="open drawer"
              edge="start"
              onClick={() => setMobileOpen(!mobileOpen)}
              sx={{ mr: 2 }}
            >
              <MenuIcon />
            </IconButton>
          )}
          <Logo />
          <Typography variant="h6" noWrap component="div" sx={{ ml: 2 }}>
            Ella IMS
          </Typography>
          <Box sx={{ flexGrow: 1 }} />
          <Box sx={{ minWidth: 0 }}>
            <DeploymentIdentity />
          </Box>
        </Toolbar>
      </AppBar>

      <Drawer
        variant={isMobile ? "temporary" : "permanent"}
        open={isMobile ? mobileOpen : true}
        onClose={() => setMobileOpen(false)}
        ModalProps={{ keepMounted: true }}
        sx={{
          "& .MuiDrawer-paper": {
            width: drawerWidth,
            boxSizing: "border-box",
            display: "flex",
            flexDirection: "column",
          },
        }}
      >
        <Toolbar />
        <Box
          component="nav"
          aria-label="Main"
          sx={{ flexGrow: 1, overflow: "auto" }}
        >
          <List>
            {navItems.map(({ to, label, icon }) => {
              const current = pathname.startsWith(to);
              return (
                <ListItem key={to} disablePadding>
                  <ListItemButton
                    component={Link}
                    to={to}
                    selected={current}
                    aria-current={current ? "page" : undefined}
                    onClick={handleNavClick}
                    sx={drawerSelectedSx}
                  >
                    <ListItemIcon>{icon}</ListItemIcon>
                    <ListItemText primary={label} />
                  </ListItemButton>
                </ListItem>
              );
            })}
          </List>
        </Box>

        <Divider />
        <Box>
          <List>
            <ListSubheader>Support</ListSubheader>
            <ListItem disablePadding>
              <ListItemButton
                component="a"
                href={DOCS_URL}
                target="_blank"
                rel="noreferrer"
                onClick={handleNavClick}
                sx={drawerSelectedSx}
              >
                <ListItemIcon>
                  <InfoIcon color="primary" />
                </ListItemIcon>
                <ListItemText primary="Documentation" />
                <OpenInNewIcon
                  sx={{ fontSize: 16, ml: 1, color: "action.active" }}
                />
              </ListItemButton>
            </ListItem>
            <ListItem disablePadding>
              <ListItemButton
                component="a"
                href={NEW_ISSUE_URL}
                target="_blank"
                rel="noreferrer"
                onClick={handleNavClick}
                sx={drawerSelectedSx}
              >
                <ListItemIcon>
                  <BugReportIcon color="primary" />
                </ListItemIcon>
                <ListItemText primary="Report a bug" />
                <OpenInNewIcon
                  sx={{ fontSize: 16, ml: 1, color: "action.active" }}
                />
              </ListItemButton>
            </ListItem>
          </List>
        </Box>
      </Drawer>
      <Box
        component="main"
        id="main-content"
        tabIndex={-1}
        sx={{
          outline: "none",
          flexGrow: 1,
          minWidth: 0,
          ml: isMobile ? 0 : `${drawerWidth}px`,
          minHeight: "100vh",
          display: "flex",
          flexDirection: "column",
        }}
      >
        <Toolbar />
        <Box
          sx={{
            flexGrow: 1,
            minWidth: 0,
            width: "100%",
            pt: 6,
            pb: 4,
            maxWidth: MAX_WIDTH,
            mx: "auto",
            px: PAGE_PADDING_X,
          }}
        >
          {children}
        </Box>
        <Footer />
      </Box>
    </Box>
  );
}
