import { Navigate, Route, Routes } from "react-router-dom";
import Layout from "@/components/Layout";
import Operator from "@/pages/Operator";

export default function App() {
  return (
    <Layout>
      <Routes>
        <Route path="/operator" element={<Operator />} />
        <Route path="*" element={<Navigate to="/operator" replace />} />
      </Routes>
    </Layout>
  );
}
