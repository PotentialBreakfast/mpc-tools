// Package assets holds the padbangers.com banner shown at the top of the MPC Link settings page (from MPC Link, MIT).
package assets

import _ "embed"

// Banner is padbangers.com's header logo (white lettering, red head), 1200x180, for the settings page header.
//
//go:embed padbangers-banner.png
var Banner []byte
