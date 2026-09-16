//go:build linux

package android

import _ "embed"

// serverJar is scrcpy's server (Apache-2.0, github.com/Genymobile/scrcpy),
// version scrcpyVersion. Only Linux builds can run on a phone, so only they
// carry it.
//
//go:embed scrcpy-server-4.1.jar
var serverJar []byte
