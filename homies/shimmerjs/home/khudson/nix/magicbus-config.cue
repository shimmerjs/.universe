// Schema for the magicbusd -config JSON: which daemon modules a host
// enables plus the optional logiretch (MX Master) device settings. Vetted
// against the per-host values and exported to plain JSON at nix build time
// (magicbus.nix); the daemon reads the JSON with stdlib encoding/json only and
// fails fast on anything this schema would reject.
package magicbus

#Config: {
	modules: {
		edge:       bool
		moonlander: bool
		logiretch:  bool
	}
	// logiretch device settings; present only when a host configures them.
	// Every field is optional: absent means "leave the device alone" (the
	// module never writes a setting the config omits).
	logiretch?: #Logiretch
}

#Logiretch: {
	// bounded so the daemon's uint16/byte casts can never wrap a hand-authored
	// value (dpi -> uint16, mode/threshold/torque -> byte); the device also
	// snaps dpi to its own step list.
	dpi?: int & >=100 & <=8000
	smartShift?: {
		mode?:      int & >=0 & <=3
		threshold?: int & >=0 & <=50
		torque?:    int & >=0 & <=100
	}
	hiresWheel?: bool
	thumbwheel?: bool
	haptic?:     int & >=0 & <=100
	buttons?: [...{
		cid:   int & >=0 & <=0xFFFF
		remap: int & >=0 & <=0xFFFF
	}]
	takeoverReset?: bool
	// bounded poll cadence (khudson-constant-cost-invariant): default 120s in
	// the daemon, clamped to this range at both the schema and the module.
	batteryPollSec?: int & >=60 & <=300
}

config: #Config
