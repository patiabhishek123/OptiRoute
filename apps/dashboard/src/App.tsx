import { Navigate, Outlet, Route, Routes, useLocation } from "react-router";
import { useAuth } from "./auth/AuthContext";
import { Layout } from "./components/Layout";
import { Login } from "./pages/Login";
import { Placeholder } from "./pages/Placeholder";
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
          <Route path="/orders" element={<Placeholder title="Orders" />} />
          <Route path="/fleet" element={<Placeholder title="Fleet" />} />
          <Route path="/plans" element={<Placeholder title="Plans" />} />
          <Route path="*" element={<Navigate to="/orders" replace />} />
        </Route>
      </Route>
    </Routes>
  );
}
