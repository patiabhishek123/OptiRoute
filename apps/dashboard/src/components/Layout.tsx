import { NavLink, Outlet } from "react-router";
import { useAuth } from "../auth/AuthContext";

const nav = [
  { to: "/orders", label: "Orders" },
  { to: "/fleet", label: "Fleet" },
  { to: "/plans", label: "Plans" },
];

export function Layout() {
  const { me, logout } = useAuth();
  return (
    <div className="flex min-h-screen">
      <aside className="flex w-56 flex-col bg-slate-900 text-slate-300">
        <div className="px-5 py-5 text-lg font-semibold tracking-tight text-white">OptiRoute</div>
        <nav className="flex-1 space-y-1 px-3">
          {nav.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              className={({ isActive }) => `block rounded-md px-3 py-2 text-sm font-medium ${isActive ? "bg-slate-800 text-white" : "hover:bg-slate-800/60 hover:text-white"}`}
            >
              {n.label}
            </NavLink>
          ))}
        </nav>
        <div className="border-t border-slate-800 px-5 py-4 text-xs">
          <p className="truncate font-medium text-slate-200">{me?.organization.name}</p>
          <p className="text-slate-400">{me?.user.role.toLowerCase()} · {me?.organization.timezone}</p>
          <button onClick={() => void logout()} className="mt-2 text-slate-400 underline hover:text-white">
            Sign out
          </button>
        </div>
      </aside>
      <main className="flex-1 overflow-x-hidden p-6">
        <Outlet />
      </main>
    </div>
  );
}
