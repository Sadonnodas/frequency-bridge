package spectera

// Wire types mirroring the subset of SSCv2 fields the adapter consumes in
// Phase 2 slice 1 (read-only path). JSON tags match the field names in the
// fakeserver — see the corresponding "What this fake DOES NOT yet match"
// note in fakeserver/fakeserver.go for fields we plan to align with the
// real OpenAPI YAML in slice 2.

// Input is one entry in /api/audio/inputs (audio coming from a transmitter
// pack into the base — i.e. an "rx" channel from the user's perspective).
type Input struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Frequency int    `json:"frequency"` // kHz; aligned with /api/rf/channels in the spec
	Mute      bool   `json:"mute"`
	GainDB    int    `json:"gain_db"`
	Encrypted bool   `json:"encrypted"`
}
