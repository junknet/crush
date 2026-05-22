# Crush Mobile

Native Android client for a local Crush API server.

This directory was seeded from ChatterUI at commit `839dab2` and keeps its
AGPL-3.0 license. The first screen has been replaced with a Crush-specific
control console:

- workspace auto-discovery from `GET /v1/workspaces`
- session list and message history
- prompt send and cancel
- permission approval cards
- sub-agent activity feed from `agent_event` SSE payloads

## Run

Start the server from the Crush repo:

```sh
scripts/start_server.sh
```

Then run Android:

```sh
cd mobile/crush_mobile
npm install
EXPO_PUBLIC_CRUSH_SERVER_URL=http://<pc-lan-ip>:28080 npm run dev:android
```

The server URL is also editable inside the app.
