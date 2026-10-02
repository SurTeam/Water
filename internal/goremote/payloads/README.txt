Embedded remote server payloads are generated here by scripts/build-go-embedded-servers.sh.

Expected filenames:
- water-server-darwin-arm64
- water-server-darwin-amd64
- water-server-linux-arm64
- water-server-linux-amd64

This README intentionally keeps the directory non-empty so go:embed compiles
before release payloads are generated.
