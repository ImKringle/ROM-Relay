import { useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import { api } from "../lib/api";
import { useSession } from "../lib/session";
import { BackLink } from "../components/BackLink";
import { ErrorNote, Hero, Icon, icons, Spinner, useData } from "../components/ui";

// Setup: shown until the first catalog scan finishes; watches readiness and
// re-checks the session the moment the relay is browsable.
export function Setup() {
  const { refresh } = useSession();
  const [stage, setStage] = useState("Preparing catalog");
  useEffect(() => {
    let stop = false;
    const poll = async () => {
      try {
        const h = await (await fetch("/healthz")).json();
        if (h.ready) return void refresh();
        setStage(h.setup_stage || "Preparing catalog");
      } catch {
        /* mid-restart */
      }
      if (!stop) setTimeout(poll, 1000);
    };
    poll();
    return () => {
      stop = true;
    };
  }, [refresh]);
  return (
    <div className="grid min-h-dvh place-items-center p-4">
      <div className="crt grid max-w-md gap-4 p-10 text-center">
        <img src="/assets/logo/brand.png" alt="" className="relative z-10 mx-auto size-16" />
        <p className="pixel relative z-10 m-0 text-sm text-cyan">LOADING…</p>
        <p className="relative z-10 m-0 text-sm text-muted">{stage}. The library opens on its own when the first scan finishes.</p>
        <div className="relative z-10 h-3 overflow-hidden rounded-sm border-2 border-line bg-void">
          <div className="h-full w-1/3 animate-pulse bg-cyan" />
        </div>
      </div>
    </div>
  );
}

type Choice = { id: string; game_id: string; name: string; encrypted: boolean; encrypted_url: string; mobile_url?: string; admin: boolean; decrypt_ready: boolean; decrypt_reason?: string };

// DownloadChoice: an encrypted CIA works on FBI / a real 3DS but usually not
// in mobile emulators, so the difference is spelled out before the download.
export function DownloadChoice() {
  const { id = "" } = useParams();
  const { data, error, loading, reload } = useData(() => api.get<Choice>(`/api/files/${encodeURIComponent(id)}/choice`), [id]);
  const [out, setOut] = useState("");
  if (error) return <ErrorNote error={error} onRetry={reload} />;
  if (loading || !data) return <Spinner />;
  if (!data.encrypted) {
    window.location.replace(data.encrypted_url);
    return <Spinner label="Downloading" />;
  }
  return (
    <>
      <BackLink to={`/games/${data.game_id}`} label="Game details" />
      <Hero eyebrow="Encrypted CIA" title="Choose the compatible download">
        <strong className="text-text">Encrypted</strong> is right for FBI and a real Nintendo 3DS. <strong className="text-text">Unencrypted</strong> is usually required by mobile 3DS emulators.
      </Hero>
      <div className="panel grid max-w-2xl gap-3 p-5">
        <a className="btn btn-primary justify-self-start" href={data.encrypted_url}>
          <Icon d={icons.download} /> Original encrypted CIA
        </a>
        {data.mobile_url ? (
          <a className="btn justify-self-start" href={data.mobile_url}>
            <Icon d={icons.download} /> Prepared unencrypted CIA
          </a>
        ) : !data.decrypt_ready ? (
          <p className="m-0 text-sm text-muted">Unencrypted preparation is unavailable: {data.decrypt_reason}.</p>
        ) : data.admin ? (
          <>
            <button
              className="btn justify-self-start"
              onClick={async () => {
                setOut("Preparing…");
                try {
                  await api.post(`/api/admin/decrypt/${encodeURIComponent(data.id)}`);
                  setOut("Preparing an unencrypted copy; progress shows in the activity tray. Reload this page when it finishes.");
                } catch (e) {
                  setOut((e as Error).message);
                }
              }}
            >
              Prepare unencrypted CIA
            </button>
            <p className="m-0 text-xs text-faint">Runs the decryptor against a copy; the encrypted source is not changed.</p>
          </>
        ) : (
          <p className="m-0 text-sm text-muted">An administrator has to prepare an unencrypted copy first.</p>
        )}
        {out && <p className="m-0 text-sm text-muted">{out}</p>}
        <Link to="/" className="text-sm">
          Back to the library
        </Link>
      </div>
    </>
  );
}
