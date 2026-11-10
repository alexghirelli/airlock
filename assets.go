// Package airlock carries the files that are built into the airlock binary:
// the image sources, the generated-instructions template and the agent team.
package airlock

import "embed"

// Assets holds image/ and team/. The build context for the base image is
// assembled from it, so the binary can build the image on any machine.
//
//go:embed image/Dockerfile image/entrypoint.sh image/init.sh image/session.sh image/herdr-up.sh image/profile-path.sh image/pi-wrapper.sh image/npm-wrapper.sh image/AGENTS.md.tmpl team
var Assets embed.FS
