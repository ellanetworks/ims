import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import Calls from "@/pages/Calls";
import Cores from "@/pages/Cores";
import Operator from "@/pages/Operator";
import Registrations from "@/pages/Registrations";

export default function App() {
  return (
    <Layout>
      <Routes>
        <Route path="/cores" element={<Cores />} />
        <Route path="/operator" element={<Operator />} />
        <Route path="/registrations" element={<Registrations />} />
        <Route path="/calls" element={<Calls />} />
        <Route path="*" element={<Navigate to="/cores" replace />} />
      </Routes>
    </Layout>
  );
}
