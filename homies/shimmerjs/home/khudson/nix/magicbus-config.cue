// Schema for the magicbusd -config JSON. magicbus is the naive transport
// backbone: it validates only WHICH modules run. Per-module device config
// is opaque here -- the app that owns a module writes its own section
// (magicbus merges it in verbatim) and the daemon validates it with stdlib
// encoding/json only (cuelang.org/go must NOT enter the TCC binary). The
// daemon fails fast on anything it cannot parse.
package magicbus

#Config: {
	modules: {
		edge:       bool
		moonlander: bool
		logiretch:  bool
	}
	// per-module config sections (e.g. logiretch: {...}) pass through
	// unvalidated by magicbus -- the owning app's option types bound the
	// values before they reach here.
	...
}

config: #Config
