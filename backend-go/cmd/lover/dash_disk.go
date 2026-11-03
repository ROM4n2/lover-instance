//go:build !embedded

package main

// A source build serves the dashboard by compiling cmd/server. That needs the Go
// toolchain and the source tree, both of which a source build has by definition.

const dashboardEmbedded = false

func init() { startDashboardServer = buildAndSuperviseServer }
