package attachment

// The carrier is the private socket pair between `p attach` and its helper.
// It carries terminal frames (api.FrameConn): the client's init frame, its
// input and resizes, and the helper's ready, output and error frames.

// initiation is the client's init frame: where the daemon is, the pending
// attachment's token and the terminal's size.
type initiation struct {
	Socket string `json:"socket"`
	Token  string `json:"token"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}
