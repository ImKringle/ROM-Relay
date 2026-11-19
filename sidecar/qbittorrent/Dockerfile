# qBittorrent sidecar for ROM Relay's torrent grabs. Reachable only on the
# internal compose network (never published), so its Web UI skips auth for
# those clients, the same trust model as the other sidecars. ROM Relay keeps
# it leech-only per torrent (zero share ratio, removed once a file finishes).
FROM qbittorrentofficial/qbittorrent-nox:5.3.0-2
# Seeded only when the profile has no config yet; the image's entrypoint
# leaves an existing file alone.
COPY qBittorrent.conf /config/qBittorrent/config/qBittorrent.conf
