import type { ReactNode } from "react";
import { NavLink, Outlet } from "react-router";
import { useAuth } from "../auth/AuthContext";

// 18px line icons, drawn with currentColor so the active state can tint them.
const icon = (d: ReactNode) => (
  <svg aria-hidden viewBox="0 0 24 24" className="size-[18px] shrink-0" fill="none" stroke="currentColor" strokeWidth={1.6} strokeLinecap="round" strokeLinejoin="round">
    {d}
  </svg>
);

const nav = [
  { to: "/orders", label: "Orders", icon: icon(<><path d="M4 7.5 12 3l8 4.5v9L12 21l-8-4.5z" /><path d="M4 7.5 12 12l8-4.5M12 12v9" /></>) },
  { to: "/fleet", label: "Fleet", icon: icon(<><path d="M3 6h11v10H3zM14 10h4l3 3v3h-7" /><circle cx="7" cy="17.5" r="1.8" /><circle cx="17" cy="17.5" r="1.8" /></>) },
  { to: "/plans", label: "Plans", icon: icon(<><circle cx="6" cy="18" r="2.2" /><circle cx="18" cy="6" r="2.2" /><path d="M8.2 18H15a3 3 0 0 0 0-6H9a3 3 0 0 1 0-6h6.8" /></>) },
];

export function Layout() {
  const { me, logout } = useAuth();
  return (
    <div className="flex min-h-screen">
      <aside className="sticky top-0 flex h-screen w-60 shrink-0 flex-col border-r border-line bg-cream">
        <div className="px-6 pt-6 pb-8 text-2xl tracking-tight text-ink">OptiRoute</div>
        <nav className="flex-1 space-y-0.5 px-3">
          {nav.map((n) => (
            <NavLink
              key={n.to}
              to={n.to}
              className={({ isActive }) =>
                `group flex items-center gap-3 rounded-sm px-3 py-2 text-sm transition-colors ${isActive ? "bg-sand font-medium text-ink" : "text-ink-muted hover:bg-sand/60 hover:text-ink"}`
              }
            >
              {({ isActive }) => (
                <>
                  <span className={isActive ? "text-plum" : "text-ink-soft group-hover:text-ink-muted"}>{n.icon}</span>
                  {n.label}
                </>
              )}
            </NavLink>
          ))}
        </nav>
        <div className="m-3 rounded-sm bg-sun p-4 text-ink">
          <p className="truncate text-lg leading-tight tracking-tight">{me?.organization.name}</p>
          <p className="mt-1 text-xs text-ink/70">{me?.user.role.toLowerCase()} · {me?.organization.timezone}</p>
          <button onClick={() => void logout()} className="mt-3 w-full rounded-sm bg-cream px-3 py-1.5 text-left text-sm font-medium hover:bg-white">
            Sign out
          </button>
        </div>
      </aside>
      <main className="flex-1 overflow-x-hidden p-8">
        <Outlet />
      </main>
    </div>
  );
}
