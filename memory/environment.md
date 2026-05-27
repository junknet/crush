# Environment & Troubleshooting

- **Proxy/Network**: Errors involving IP `198.18.0.11` typically indicate local proxy (Clash/TUN) issues or DNS Fake-IP mapping failures rather than remote provider downtime.
- **Build Commands**:
    - Build scheduler and agent: `go build ./internal/scheduler/... ./internal/agent/...`
    - Build UI: `go build ./internal/ui/...`
- **Test Commands**:
    - Run scheduler tests: `go test -count=1 -timeout=60s ./internal/scheduler/...`
    - Run UI/model tests: `go test -count=1 -timeout=60s ./internal/ui/model/...`
    - Full test suite (excluding scratch): `go test -count=1 -timeout=120s $(go list ./... | grep -v -E '/scratch')`
- **Known Issues**:
    - `TestConfig_*` failures in `internal/config/load_test.go` are often due to local config pollution (e.g., custom providers in `~/.config/crush/crush.yaml`) and are unrelated to recent agent changes.
