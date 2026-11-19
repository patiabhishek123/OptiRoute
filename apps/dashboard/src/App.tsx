import { Navigate, Outlet, Route, Routes, useLocation } from "react-router";
import { useAuth } from "./auth/AuthContext";
import { Layout } from "./components/Layout";
import { Login } from "./pages/Login";
import { Fleet } from "./pages/Fleet";
import { Orders } from "./pages/Orders";
import { PlanDetail } from "./pages/PlanDetail";
import { Plans } from "./pages/Plans";
import { Signup } from "./pages/Signup";

function RequireAuth() {
  const { me, loading } = useAuth();
  const location = useLocation();
  if (loading) return <div className="p-6 text-sm text-slate-500">Loading…</div>;
  if (!me) return <Navigate to="/login" replace state={{ from: location }} />;
  return <Outlet />;
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login />} />
      <Route path="/signup" element={<Signup />} />
      <Route element={<RequireAuth />}>
        <Route element={<Layout />}>
          <Route path="/orders" element={<Orders />} />
          <Route path="/fleet" element={<Fleet />} />
          <Route path="/plans" element={<Plans />} />
          <Route path="/plans/:id" element={<PlanDetail />} />
          <Route path="*" element={<Navigate to="/orders" replace />} />
        </Route>
      </Route>
    </Routes>
  );
}
