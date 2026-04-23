package cmd

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
)

// vmSFTPHandler implements pkg/sftp Handlers interfaces,
// routing SFTP operations to the VM via the infrabox API.
type vmSFTPHandler struct {
	vmName string
}

// Fileread returns an error — we don't support reading files via SFTP.
func (h *vmSFTPHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	logSSHProxy("sftp: Fileread %s (unsupported)", r.Filepath)
	return nil, sftp.ErrSSHFxPermissionDenied
}

// Filewrite returns a buffer for writing. On Close, the file is uploaded to the VM.
func (h *vmSFTPHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	logSSHProxy("sftp: Filewrite %s", r.Filepath)
	return &vmSFTPFile{vmName: h.vmName, path: r.Filepath}, nil
}

// Filecmd handles SFTP commands: Mkdir, Setstat (chmod), Remove, Rename.
func (h *vmSFTPHandler) Filecmd(r *sftp.Request) error {
	logSSHProxy("sftp: Filecmd %s %s", r.Method, r.Filepath)
	switch r.Method {
	case "Mkdir":
		_, _, err := sftpRunCommand(h.vmName, "mkdir -p "+shquote(r.Filepath))
		return sftpAPIErr(err)
	case "Setstat":
		if r.AttrFlags().Permissions {
			perm := r.Attributes().FileMode().Perm()
			_, _, err := sftpRunCommand(h.vmName, fmt.Sprintf("chmod %04o %s", perm, shquote(r.Filepath)))
			return sftpAPIErr(err)
		}
	case "Remove":
		_, _, err := sftpRunCommand(h.vmName, "rm -f "+shquote(r.Filepath))
		return sftpAPIErr(err)
	case "Rename":
		_, _, err := sftpRunCommand(h.vmName, fmt.Sprintf("mv %s %s", shquote(r.Filepath), shquote(r.Target)))
		return sftpAPIErr(err)
	}
	return nil
}

// Fileinfo handles STAT/LSTAT.
// Returns not-found for files (so Claude Code will create/upload them),
// but returns a fake directory entry for directory paths.
func (h *vmSFTPHandler) Fileinfo(r *sftp.Request) ([]os.FileInfo, error) {
	logSSHProxy("sftp: Fileinfo %s %s", r.Method, r.Filepath)
	// Run stat on the VM to get real info
	out, exitCode, err := sftpRunCommand(h.vmName,
		fmt.Sprintf("stat -c '%%s %%f %%Y %%n' %s 2>/dev/null", shquote(r.Filepath)))
	if err != nil || exitCode != 0 || len(out) == 0 {
		return nil, sftp.ErrSSHFxNoSuchFile
	}
	// Parse: size hex-mode mtime name
	var size int64
	var modeHex, name string
	var mtime int64
	fmt.Sscanf(strings.TrimSpace(string(out)), "%d %s %d %s", &size, &modeHex, &mtime, &name)
	modeVal, _ := strconv.ParseUint(modeHex, 16, 32)
	return []os.FileInfo{&sftpFileInfo{
		name:  filepath.Base(r.Filepath),
		size:  size,
		mode:  os.FileMode(modeVal),
		mtime: time.Unix(mtime, 0),
	}}, nil
}

// Filelist handles directory listing.
func (h *vmSFTPHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	logSSHProxy("sftp: Filelist %s", r.Filepath)
	return sftpEmptyLister{}, nil
}

// --- vmSFTPFile ---

type vmSFTPFile struct {
	vmName string
	path   string
	mu     sync.Mutex
	data   []byte
}

func (f *vmSFTPFile) WriteAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	end := int(off) + len(p)
	if end > len(f.data) {
		f.data = append(f.data, make([]byte, end-len(f.data))...)
	}
	copy(f.data[int(off):], p)
	return len(p), nil
}

func (f *vmSFTPFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if int(off) >= len(f.data) {
		return 0, io.EOF
	}
	n := copy(p, f.data[int(off):])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Close uploads the buffered file data to the VM via the files API.
func (f *vmSFTPFile) Close() error {
	f.mu.Lock()
	data := make([]byte, len(f.data))
	copy(data, f.data)
	f.mu.Unlock()

	logSSHProxy("sftp: Close %s (%d bytes) — uploading", f.path, len(data))
	return uploadFileToVM(f.vmName, f.path, data)
}

// uploadFileToVM creates a tar archive and uploads via POST /v1/vms/{name}/files.
func uploadFileToVM(vmName, filePath string, data []byte) error {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     filepath.Base(filePath),
		Size:     int64(len(data)),
		Mode:     0755,
		Uid:      1000,
		Gid:      1000,
	}); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	tw.Close()

	destDir := filepath.Dir(filePath)
	apiURL := strings.TrimRight(cfg.Endpoint, "/") +
		"/v1/vms/" + vmName + "/files?path=" + url.QueryEscape(destDir)

	req, err := http.NewRequest(http.MethodPost, apiURL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", cfg.APIKey)

	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	logSSHProxy("sftp: upload %s status=%d", filePath, resp.StatusCode)
	return nil
}

// sftpRunCommand runs a shell command on the VM via the /run API.
func sftpRunCommand(vmName, command string) ([]byte, int, error) {
	apiURL := strings.TrimRight(cfg.Endpoint, "/") + "/v1/vms/" + vmName + "/run"
	body, _ := json.Marshal(map[string]string{"command": command})
	req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, 1, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.APIKey)

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, 1, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	exitCode, _ := strconv.Atoi(resp.Header.Get("X-Exit-Code"))
	return out, exitCode, nil
}

func sftpAPIErr(err error) error {
	if err != nil {
		return sftp.ErrSSHFxFailure
	}
	return nil
}

// shquote safely quotes a shell argument with single quotes.
func shquote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// --- helpers ---

type sftpFileInfo struct {
	name  string
	size  int64
	mode  os.FileMode
	mtime time.Time
}

func (fi *sftpFileInfo) Name() string      { return fi.name }
func (fi *sftpFileInfo) Size() int64       { return fi.size }
func (fi *sftpFileInfo) Mode() os.FileMode { return fi.mode }
func (fi *sftpFileInfo) ModTime() time.Time { return fi.mtime }
func (fi *sftpFileInfo) IsDir() bool       { return fi.mode.IsDir() }
func (fi *sftpFileInfo) Sys() any          { return nil }

type sftpEmptyLister struct{}

func (sftpEmptyLister) ListAt(_ []os.FileInfo, _ int64) (int, error) {
	return 0, io.EOF
}
