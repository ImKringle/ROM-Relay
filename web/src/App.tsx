import { BrowserRouter, Navigate, Outlet, Route, Routes, useLocation } from "react-router";
import { canBrowse, SessionProvider, useSession } from "./lib/session";
import { Shell } from "./components/Shell";
import { Spinner } from "./components/ui";
import { Login } from "./pages/Login";
import { Library } from "./pages/Library";
import { GamePage } from "./pages/Game";
import { Saves } from "./pages/Saves";
import { Account, PasswordPage } from "./pages/Account";
import { Torrents } from "./pages/Torrents";
import { Patcher } from "./pages/Patcher";
import { Randomizer } from "./pages/Randomizer";
import { DownloadChoice, Setup } from "./pages/Misc";
import { Pokedex } from "./pages/pokemon/Pokedex";
import { Trainers } from "./pages/pokemon/Trainers";
import { TypeChart } from "./pages/pokemon/TypeChart";
import { PokeHome } from "./pages/pokemon/PokeHome";
import { Editor } from "./pages/pokemon/Editor";
import { AdminHub } from "./pages/admin/AdminHub";
import { AdminRoms } from "./pages/admin/AdminRoms";
import { Users } from "./pages/admin/Users";
import { ApiKeys } from "./pages/admin/ApiKeys";
import { PlayerData } from "./pages/admin/PlayerData";
import { Settings } from "./pages/admin/Settings";
import { TorrentSources } from "./pages/admin/TorrentSources";
import { PatchWorkspace } from "./pages/admin/PatchWorkspace";

export function App() {
  return (
    <SessionProvider>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route element={<Gate />}>
            <Route element={<Shell />}>
              <Route index element={<Library />} />
              <Route path="games/:id" element={<GamePage />} />
              <Route path="download/:id" element={<DownloadChoice />} />
              <Route path="saves" element={<Saves />} />
              <Route path="torrents" element={<Torrents />} />
              <Route path="romm" element={<Navigate to="/?source=romm" replace />} />
              <Route path="patches" element={<Patcher />} />
              <Route path="randomizer" element={<Randomizer />} />
              <Route path="pokedex" element={<Pokedex />} />
              <Route path="pokedex/types" element={<TypeChart />} />
              <Route path="trainers" element={<Trainers />} />
              <Route path="pokehome" element={<PokeHome />} />
              <Route path="editor" element={<Editor />} />
              <Route path="account" element={<Account />} />
              <Route path="account/webdav" element={<Account />} />
              <Route path="account/password" element={<PasswordPage />} />
              <Route path="admin" element={<Gate admin />}>
                <Route index element={<AdminHub />} />
                <Route path="users" element={<Users />} />
                <Route path="roms" element={<AdminRoms />} />
                <Route path="saves" element={<PlayerData />} />
                <Route path="settings" element={<Settings />} />
                <Route path="api-keys" element={<ApiKeys />} />
                <Route path="torrents" element={<TorrentSources />} />
                <Route path="patches" element={<PatchWorkspace />} />
              </Route>
              <Route path="*" element={<NotFound />} />
            </Route>
          </Route>
        </Routes>
      </BrowserRouter>
    </SessionProvider>
  );
}

// Gate mirrors the server's access model in the UI: pages need a session (or
// an open relay), a temporary password must be changed first, and admin pages
// need an admin. The server enforces all of it again on every API call; this
// only decides what to draw.
function Gate({ admin }: { admin?: boolean }) {
  const { session } = useSession();
  const location = useLocation();
  if (!session) return <Spinner label="Starting" />;
  if (!session.ready) return <Setup />;
  if (!canBrowse(session)) return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  if (session.must_change_password && location.pathname !== "/account/password") return <Navigate to="/account/password" replace />;
  if (admin && !session.admin) return <NotFound />;
  return <Outlet />;
}

function NotFound() {
  return (
    <div className="crt grid place-items-center gap-4 p-16 text-center">
      <p className="pixel relative z-10 m-0 text-cyan">GAME OVER</p>
      <p className="relative z-10 m-0 text-sm text-muted">That page does not exist, or you do not have access to it.</p>
      <a className="btn relative z-10" href="/">
        Continue?
      </a>
    </div>
  );
}
