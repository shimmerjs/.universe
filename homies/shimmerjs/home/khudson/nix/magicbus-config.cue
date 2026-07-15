// Schema for the khudson-touchd -config JSON: which daemon modules a host
// enables. Vetted against the per-host values and exported to plain JSON at
// nix build time (magicbus.nix); the daemon reads the JSON with stdlib
// encoding/json only and fails fast on anything this schema would reject.
package magicbus

#Config: {
	modules: {
		edge:       bool
		moonlander: bool
	}
}

config: #Config
