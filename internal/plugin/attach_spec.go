package plugin

// AttachSpec contains only daemon-selected fixed execution data. Socket is
// available solely on the private helper connection, never in a public spec.
type AttachSpec struct {
	Project  string   `json:"project"`
	Instance string   `json:"instance"`
	Argv     []string `json:"argv"`
}
