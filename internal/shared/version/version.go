// Package version holds the build version of all three binaries.
//
// It is set at build time by the Dockerfiles:
//
//	go build -ldflags "-X github.com/macsnoeren/juiceshop-ctf/internal/shared/version.Version=0.1.0"
//
// The agent sends it in every report (protocol.Report.AgentVersion), so the
// operator can see in /admin/ which instance runs which build. Without the
// -ldflags it is "dev", which is what a `go run` on a laptop reports.
package version

// Version is the build version, "dev" unless set by -ldflags.
var Version = "dev"
