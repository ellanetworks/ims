import type { ReactElement } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import { AuthProvider, useAuth, type Permission } from "@/contexts/AuthContext";
import Calls from "@/pages/Calls";
import Cores from "@/pages/Cores";
import Initialize from "@/pages/Initialize";
import Login from "@/pages/Login";
import Operator from "@/pages/Operator";
import Profile from "@/pages/Profile";
import Registrations from "@/pages/Registrations";
import UserDetail from "@/pages/UserDetail";
import Users from "@/pages/Users";
import PushToTalk from "@/pages/mcptt/PushToTalk";

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/initialize" element={<Initialize />} />
      <Route
        path="*"
        element={
          <AuthProvider>
            <Layout>
              <Pages />
            </Layout>
          </AuthProvider>
        }
      />
    </Routes>
  );
}

const pages: { path: string; element: ReactElement; permission: Permission }[] =
  [
    { path: "/cores", element: <Cores />, permission: "diameter:read" },
    { path: "/operator", element: <Operator />, permission: "operator:read" },
    {
      path: "/registrations",
      element: <Registrations />,
      permission: "registration:read",
    },
    { path: "/calls", element: <Calls />, permission: "call_record:read" },
    {
      path: "/ptt",
      element: <Navigate to="/ptt/groups" replace />,
      permission: "mcptt:read",
    },
    { path: "/ptt/:tab", element: <PushToTalk />, permission: "mcptt:read" },
    { path: "/users", element: <Users />, permission: "user:admin" },
    {
      path: "/users/:email",
      element: <UserDetail />,
      permission: "user:admin",
    },
  ];

// Pages are the pages of the logged-in user's role. Any other path goes to its first page.
function Pages() {
  const auth = useAuth();
  const home = auth.can("diameter:read") ? "/cores" : "/profile";

  return (
    <Routes>
      {pages
        .filter(({ permission }) => auth.can(permission))
        .map(({ path, element }) => (
          <Route key={path} path={path} element={element} />
        ))}
      <Route path="/profile" element={<Profile />} />
      <Route path="*" element={<Navigate to={home} replace />} />
    </Routes>
  );
}
