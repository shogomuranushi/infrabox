package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractPathCandidates(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "simple absolute path",
			in:   "/Users/me/pic.png",
			want: []string{"/Users/me/pic.png"},
		},
		{
			name: "home-relative path",
			in:   "look at ~/Pictures/foo.jpg please",
			want: []string{"~/Pictures/foo.jpg"},
		},
		{
			name: "shell-escaped spaces",
			in:   `/Users/me/my\ file.png`,
			want: []string{"/Users/me/my file.png"},
		},
		{
			name: "double-quoted with spaces",
			in:   `"/Users/me/with space.png"`,
			want: []string{"/Users/me/with space.png"},
		},
		{
			name: "single-quoted",
			in:   `'/tmp/a b.png'`,
			want: []string{"/tmp/a b.png"},
		},
		{
			name: "prose with trailing punctuation",
			in:   "see /tmp/foo.png, it works.",
			want: []string{"/tmp/foo.png"},
		},
		{
			name: "dedup",
			in:   "/tmp/a.png /tmp/a.png /tmp/b.png",
			want: []string{"/tmp/a.png", "/tmp/b.png"},
		},
		{
			name: "non-absolute ignored",
			in:   "foo.png ./rel.png",
			want: nil,
		},
		{
			name: "windows drive",
			in:   `C:\Users\me\file.png`,
			want: []string{`C:\Users\me\file.png`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractPathCandidates(tc.in)
			if !equalStringSlice(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeUploader records calls for assertion.
type fakeUploader struct {
	calls []struct {
		vm, dest, local string
	}
	err error
}

func (f *fakeUploader) upload(vm, dest, local string, recursive bool) error {
	f.calls = append(f.calls, struct{ vm, dest, local string }{vm, dest, local})
	return f.err
}

func TestPasteInterceptor_PassThroughNonPaste(t *testing.T) {
	var forwarded bytes.Buffer
	p := newPasteInterceptor("vm", func(b []byte) error {
		forwarded.Write(b)
		return nil
	}, func(string, ...interface{}) {})
	p.upload = func(string, string, string, bool) error { t.Fatal("upload should not be called"); return nil }

	inputs := [][]byte{
		[]byte("hello "),
		[]byte("world\r"),
	}
	for _, in := range inputs {
		if err := p.Feed(in); err != nil {
			t.Fatal(err)
		}
	}
	if got := forwarded.String(); got != "hello world\r" {
		t.Errorf("unexpected forwarded: %q", got)
	}
}

func TestPasteInterceptor_ChunkBoundaryInsideStartMarker(t *testing.T) {
	var forwarded bytes.Buffer
	p := newPasteInterceptor("vm", func(b []byte) error {
		forwarded.Write(b)
		return nil
	}, func(string, ...interface{}) {})
	p.upload = func(string, string, string, bool) error { return nil }

	// Split "prefix\x1b[200~body\x1b[201~" across the start marker.
	full := []byte("prefix\x1b[200~body-no-path\x1b[201~")
	for i := 1; i < len(full); i++ {
		forwarded.Reset()
		p = newPasteInterceptor("vm", func(b []byte) error {
			forwarded.Write(b)
			return nil
		}, func(string, ...interface{}) {})
		p.upload = func(string, string, string, bool) error { return nil }

		if err := p.Feed(full[:i]); err != nil {
			t.Fatalf("split %d: %v", i, err)
		}
		if err := p.Feed(full[i:]); err != nil {
			t.Fatalf("split %d: %v", i, err)
		}
		if err := p.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
		if got := forwarded.String(); got != string(full) {
			t.Errorf("split at %d: forwarded %q, want %q", i, got, string(full))
		}
	}
}

func TestPasteInterceptor_UploadsMatchingPath(t *testing.T) {
	// Create a real file in a temp dir that we will claim is under $HOME.
	dir := t.TempDir()
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", dir)
	defer os.Setenv("HOME", origHome)

	pic := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(pic, []byte("fake image"), 0644); err != nil {
		t.Fatal(err)
	}

	var forwarded bytes.Buffer
	var uploaded []string
	p := newPasteInterceptor("my-vm", func(b []byte) error {
		forwarded.Write(b)
		return nil
	}, func(string, ...interface{}) {})
	p.upload = func(vm, dest, local string, recursive bool) error {
		uploaded = append(uploaded, local)
		return nil
	}
	// Pre-allow so confirm() is not called from the test.
	resolved, _ := filepath.EvalSymlinks(pic)
	p.allowed[resolved] = true

	paste := "\x1b[200~" + resolved + "\x1b[201~"
	if err := p.Feed([]byte(paste)); err != nil {
		t.Fatal(err)
	}
	if forwarded.String() != paste {
		t.Errorf("forwarded %q, want %q", forwarded.String(), paste)
	}
	if len(uploaded) != 1 || uploaded[0] != resolved {
		t.Errorf("uploaded %v, want [%s]", uploaded, resolved)
	}
}

func TestValidateSourcePath_RejectsOutsideHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	// File outside HOME
	other := t.TempDir()
	p := filepath.Join(other, "foo.png")
	os.WriteFile(p, []byte("x"), 0644)

	if _, _, err := validateSourcePath(p); err == nil || !strings.Contains(err.Error(), "outside home") {
		t.Errorf("expected 'outside home' error, got %v", err)
	}
}

func TestValidateSourcePath_RejectsSensitivePath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	ssh := filepath.Join(dir, ".ssh")
	os.MkdirAll(ssh, 0700)
	p := filepath.Join(ssh, "notes.txt")
	os.WriteFile(p, []byte("x"), 0644)

	if _, _, err := validateSourcePath(p); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Errorf("expected sensitive error, got %v", err)
	}
}

func TestValidateSourcePath_RejectsSymlinkFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	real := filepath.Join(dir, "real.png")
	os.WriteFile(real, []byte("x"), 0644)
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported")
	}

	if _, _, err := validateSourcePath(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("expected symlink error, got %v", err)
	}
}

func TestValidateSourcePath_RejectsDisallowedExtension(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	p := filepath.Join(dir, "thing.exe")
	os.WriteFile(p, []byte("x"), 0644)

	if _, _, err := validateSourcePath(p); err == nil || !strings.Contains(err.Error(), "extension") {
		t.Errorf("expected extension error, got %v", err)
	}
}

func TestValidateSourcePath_AllowsNormalImage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	p := filepath.Join(dir, "hello.png")
	os.WriteFile(p, []byte("x"), 0644)

	abs, info, err := validateSourcePath(p)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	// On macOS $TMPDIR often lives under /var/folders which is a symlink to
	// /private/var/... so abs may be the evaluated form.
	want, _ := filepath.EvalSymlinks(p)
	if abs != want {
		t.Errorf("abs %q, want %q", abs, want)
	}
	if info.Size() != 1 {
		t.Errorf("size %d, want 1", info.Size())
	}
}
