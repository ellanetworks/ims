import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import Calls from "@/pages/Calls";
import Cores from "@/pages/Cores";
import Operator from "@/pages/Operator";
import Registrations from "@/pages/Registrations";
import MCPTTGroupCalls from "@/pages/mcptt/GroupCalls";
import MCPTTGroups from "@/pages/mcptt/Groups";
import MCPTTSettings from "@/pages/mcptt/Settings";
import MCPTTUsers from "@/pages/mcptt/Users";

export default function App() {
  return (
    <Layout>
      <Routes>
        <Route path="/cores" element={<Cores />} />
        <Route path="/operator" element={<Operator />} />
        <Route path="/registrations" element={<Registrations />} />
        <Route path="/calls" element={<Calls />} />
        <Route path="/ptt/users" element={<MCPTTUsers />} />
        <Route path="/ptt/groups" element={<MCPTTGroups />} />
        <Route path="/ptt/calls" element={<MCPTTGroupCalls />} />
        <Route path="/ptt/settings" element={<MCPTTSettings />} />
        <Route path="*" element={<Navigate to="/cores" replace />} />
      </Routes>
    </Layout>
  );
}
