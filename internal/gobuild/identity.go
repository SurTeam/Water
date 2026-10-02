package gobuild

// Variant and Version are overridden for packaged builds with:
//
//   -ldflags "-X github.com/SurTeam/Water/internal/gobuild.Variant=release //             -X github.com/SurTeam/Water/internal/gobuild.Version=0.2.32"
//
// Keeping one runtime identity source prevents the GUI, CLI, server and
// embedded remote payloads from drifting across the dev/release protocol
// boundary.
var (
	Variant = "dev"
	Version = "go-rewrite"
)
