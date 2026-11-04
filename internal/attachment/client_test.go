package attachment

import "testing"

func TestAttachmentSize(t *testing.T) {
	for _, tc := range []struct {
		name          string
		width, height int
		wantW, wantH  int
	}{
		{"script PTY without parent terminal", 0, 0, 80, 24},
		{"minimized width", 0, 40, 80, 40},
		{"oversized height", 100, 65536, 100, 24},
		{"ordinary terminal", 100, 40, 100, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, h := attachmentSize(tc.width, tc.height)
			if w != tc.wantW || h != tc.wantH {
				t.Fatalf("attachmentSize(%d, %d) = %d, %d; want %d, %d", tc.width, tc.height, w, h, tc.wantW, tc.wantH)
			}
		})
	}
}
