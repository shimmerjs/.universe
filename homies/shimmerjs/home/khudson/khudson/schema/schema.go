// Package schema embeds the khudson CUE config contract.
package schema

import _ "embed"

// Schema is the config contract every khudson config must unify with.
//
//go:embed khudson.cue
var Schema []byte

// Example is a complete config that vets clean; bus and dock fall back to
// it when started without -config.
//
//go:embed example.cue
var Example []byte
