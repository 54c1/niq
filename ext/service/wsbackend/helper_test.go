package wsbackend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePath(t *testing.T) {
	dir := t.TempDir()
	root, _ := filepath.Abs(dir)

	tests := []struct {
		name    string
		raw     string
		prep    func()
		wantErr bool
	}{
		// valid relative
		{name: "plain relative", raw: "main.go", wantErr: false},
		{name: "dot prefix", raw: "./main.go", wantErr: false},
		{name: "nested relative", raw: "a/b/c.go", wantErr: false},
		{name: "double slash", raw: "a//b.go", wantErr: false},

		// escape attempts
		{name: "parent escape", raw: "../etc/passwd", wantErr: true},
		{name: "deep parent escape", raw: "../../etc/passwd", wantErr: true},

		// absolute
		{name: "absolute inside", raw: filepath.Join(root, "main.go"), wantErr: false},
		{name: "absolute outside", raw: "/etc/passwd", wantErr: true},

		// empty
		{name: "empty", raw: "", wantErr: true},

		// symlink escape
		{
			name: "symlink escape to /etc",
			raw:  "link-out/passwd",
			prep: func() {
				os.Symlink("/etc", filepath.Join(root, "link-out"))
			},
			wantErr: true,
		},

		// ~ expansion — always outside a temp workspace
		{name: "tilde with slash", raw: "~/file.go", wantErr: true},
		{name: "tilde alone", raw: "~", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.prep != nil {
				tt.prep()
			}
			_, err := resolvePath(root, tt.raw)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolvePath(%q) error = %v, wantErr = %v", tt.raw, err, tt.wantErr)
			}
		})
	}
}
