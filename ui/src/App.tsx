import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import { AuthProvider, useAuth } from "@/contexts/AuthContext";
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

// Pages are the pages of the logged-in user's role: every role reads every page but Users.
function Pages() {
  const auth = useAuth();

  return (
    <Routes>
      <Route path="/cores" element={<Cores />} />
      <Route path="/operator" element={<Operator />} />
      <Route path="/registrations" element={<Registrations />} />
      <Route path="/calls" element={<Calls />} />
      <Route path="/ptt" element={<Navigate to="/ptt/groups" replace />} />
      <Route path="/ptt/:tab" element={<PushToTalk />} />
      <Route path="/profile" element={<Profile />} />
      {auth.can("user:admin") && (
        <>
          <Route path="/users" element={<Users />} />
          <Route path="/users/:email" element={<UserDetail />} />
        </>
      )}
      <Route path="*" element={<Navigate to="/cores" replace />} />
    </Routes>
  );
}
