# Deployment

Two scenarios. Pick whichever matches your setup.

## Single machine (the common case)

Server, vector store, metadata store, indexer — all on one host. Your MacBook, with all your local repos. This is what most users want.

```bash
git clone https://github.com/AustinSchoen/codebase-intel
cd codebase-intel
./setup.sh
```

`setup.sh` starts the server stack via Docker Compose, generates credentials, prints the `claude mcp add` command to wire it to Claude Code. From here it's just `./scripts/index.sh configs/my-project.yaml` per codebase. Done.

No daemon, no LAN, no auto-discovery. See [README.md](../README.md#quick-start) for the full walk-through.

## Multiple machines

Server runs on one always-on host (`korlat.local`, say). Indexer daemons run on each dev machine that has source code (`ams-mbp.local`, etc.) and push embeddings to the central server.

### On the server host

```bash
git clone https://github.com/AustinSchoen/codebase-intel
cd codebase-intel
./setup.sh
```

Same as the single-machine flow — the server doesn't behave differently based on whether it's accepting remote indexers. Note the bearer token in the final `claude mcp add` line; the indexer hosts will need it (but they'll usually get it auto-discovered, see below).

If your server runs in a Docker container (the `setup.sh` default), mDNS announcements don't escape the container's network namespace. For remote indexer discovery to work, either:

- Edit `docker-compose.yml` and set `network_mode: host` on the `server` service, or
- Run the server natively via systemd (skip `setup.sh`, build the binary, write your own unit referencing the Docker-managed Qdrant and Postgres)

### On each indexer host

```bash
git clone https://github.com/AustinSchoen/codebase-intel
cd codebase-intel
./setup-indexer.sh
```

What it does, in order:

1. Builds the indexer binary if missing.
2. Scans the LAN for ~3 seconds via mDNS, looking for `_codebase-intel._tcp`.
3. If exactly one server responds: uses it. Multiple: prompts you to pick. None: prompts for `--server URL` and bearer token.
4. Prompts for codebase paths (one at a time, empty input to finish).
5. Auto-detects languages from file extensions.
6. Writes per-host config under `~/.config/codebase-intel/`:
   ```
   ~/.config/codebase-intel/
   ├── daemon.yaml           # server URL, node ID
   ├── credentials.env       # mode 0600, holds MCP_API_KEY
   └── codebases.d/
       ├── <codebase>.yaml   # one per codebase
       └── …
   ```
7. Installs a user service unit (`~/Library/LaunchAgents/…` on macOS, `~/.config/systemd/user/…` on Linux) referencing the binary at `~/bin/codebase-intel-indexer` with `-config-dir` pointing at `codebases.d/`.
8. Starts the service.

Adding a codebase later: drop a new YAML in `codebases.d/` and restart the service. The unit file never needs to change.

```bash
# add a new codebase to an existing host
$EDITOR ~/.config/codebase-intel/codebases.d/new-project.yaml
# macOS
launchctl unload ~/Library/LaunchAgents/com.codebase-intel.daemon.plist
launchctl load   ~/Library/LaunchAgents/com.codebase-intel.daemon.plist
# Linux
systemctl --user restart codebase-intel-daemon.service
```

### When mDNS doesn't work

Several environments block multicast: corporate Wi-Fi, hotel Wi-Fi, some VPNs, and the Docker default network (covered above). Fall back to explicit flags:

```bash
./setup-indexer.sh --server http://192.168.1.10:8090 --token <bearer>
```

The server's mDNS advertising can also be disabled if you don't want the bearer token broadcast on your LAN. In the server's config (`configs/server.yaml`):

```yaml
server:
  discovery:
    advertise: false        # don't publish at all
    # or:
    advertise_token: false  # publish URL but not token
```

### Service templates

If you want to wire this up by hand without the script:

- macOS: [`examples/launchd/com.codebase-intel.daemon.plist`](../examples/launchd/com.codebase-intel.daemon.plist)
- Linux: [`examples/systemd/codebase-intel-daemon.service`](../examples/systemd/codebase-intel-daemon.service)

Replace the `TEMPLATE_*` placeholders, copy to the right location, load/enable.

## Diagnosing connection issues

| Symptom | Check |
|---|---|
| `setup-indexer.sh` finds no servers | `dns-sd -B _codebase-intel._tcp` (macOS) or `avahi-browse _codebase-intel._tcp -t` (Linux). If neither sees anything, mDNS is blocked between your hosts. |
| `services: []` from `./bin/codebase-intel-indexer -discover` | Same — multicast not reaching this host. |
| Daemon connects but no commands arrive | `curl -H "Authorization: Bearer $TOKEN" http://<server>:8090/api/indexers` and verify your node ID + codebases show up |
| Daemon repeatedly registers/deregisters | Laptop is sleeping. Use `caffeinate -i` on macOS during long indexing sessions or accept that watchers reconnect on wake. |
| Indexer logs `database unavailable` | The server lost its Postgres connection. Restart it. (Pre–#10 builds need this manually; post-#10 builds fail-fast on init.) |
