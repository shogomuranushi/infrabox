package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Paste interceptor: watches the stdin → WebSocket stream for bracketed paste
// sequences (ESC[200~ ... ESC[201~), looks for locally-existing absolute file
// paths inside them, and auto-uploads those files to the VM at the same path
// before releasing the paste content to the WebSocket.
//
// Security design:
//   - Opt-in only (--auto-upload flag)
//   - Detection restricted to bracketed paste regions (never raw keystrokes)
//   - Symlinks are rejected
//   - Dotfile and sensitive-directory blocklist
//   - Extension allowlist
//   - Size limit (20 MiB default)
//   - All auto-uploads recorded to ~/.ib/auto-upload.log
//   - On any parser/upload failure we fail open (pass-through) rather than
//     deadlock the ssh session.

const (
	pasteMaxBufBytes  = 1 << 20 // 1 MiB hard cap on a single paste we are willing to inspect
	maxAutoUploadSize = 20 << 20
)

var (
	pasteStart = []byte("\x1b[200~")
	pasteEnd   = []byte("\x1b[201~")
)

// Extensions we are willing to auto-upload without bumping the confirmation
// from "first-time" to "loud warning". The list is deliberately narrow.
var allowedAutoUploadExts = map[string]bool{
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".gif":  true,
	".webp": true,
	".bmp":  true,
	".pdf":  true,
	".txt":  true,
	".md":   true,
	".log":  true,
	".csv":  true,
	".json": true,
}

// Path segments that, if present anywhere in the resolved path, cause us to
// refuse auto-upload regardless of extension. Users can still use `ib scp`
// explicitly for these.
var sensitivePathSegments = []string{
	".ssh",
	".aws",
	".gnupg",
	".gpg",
	".kube",
	".docker",
	".netrc",
	".npmrc",
	".pypirc",
	".config/gh",
	"Library/Keychains",
	"Library/Application Support/Google/Chrome",
	"Library/Application Support/Firefox",
	".mozilla",
	".password-store",
}

// Sensitive file name prefixes (dotfiles like .env, .env.local, etc.)
var sensitiveBasenamePrefixes = []string{
	".env",
	"id_rsa",
	"id_ed25519",
	"id_ecdsa",
	"id_dsa",
}

// pathCandidateRe matches things that look like absolute file paths inside a
// pasted text blob. It handles:
//   - unix absolute paths starting with /
//   - home-relative paths starting with ~/
//   - windows paths like C:\...
//   - shell-escaped spaces (\ )
//
// Quoted forms are handled in a preprocessing step, not here.
var pathCandidateRe = regexp.MustCompile(
	`(?:~/|/|[A-Za-z]:[\\/])(?:\\.|[^\s"'` + "`" + `])+`,
)

// quotedPathRe extracts "…" and '…' quoted tokens so we can unquote them
// before running pathCandidateRe.
var quotedPathRe = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)

// uploader abstracts the file-upload side-effect for testability.
type uploader func(vmName, destPath, localPath string, recursive bool) error

// pasteInterceptor is a stateful io.Writer-ish filter. Call Feed with chunks
// read from stdin; it will invoke write() with chunks to forward to the
// WebSocket, possibly delayed while a paste is being processed.
type pasteInterceptor struct {
	vmName string
	upload uploader
	write  func([]byte) error

	// bracketed paste state
	inPaste bool
	buf     bytes.Buffer // holds paste content between 200~ and 201~
	carry   bytes.Buffer // holds trailing bytes that might start a marker
}

func newPasteInterceptor(vmName string, write func([]byte) error) *pasteInterceptor {
	return &pasteInterceptor{
		vmName: vmName,
		upload: uploadToVM,
		write:  write,
	}
}

// Feed ingests a chunk from stdin. It forwards non-paste bytes immediately via
// write() and buffers paste bodies. When a paste ends, it runs auto-upload
// synchronously, then flushes the paste bytes to write().
//
// Failure modes are all "fail open": any internal error causes the buffered
// bytes to be forwarded as-is so the ssh session does not lock up.
func (p *pasteInterceptor) Feed(chunk []byte) error {
	// Combine any carried bytes from the previous chunk.
	if p.carry.Len() > 0 {
		combined := append(p.carry.Bytes(), chunk...)
		p.carry.Reset()
		chunk = combined
	}

	for len(chunk) > 0 {
		if !p.inPaste {
			// Search for the start marker. We must also be careful that the
			// marker might straddle chunk boundaries.
			idx := bytes.Index(chunk, pasteStart)
			if idx < 0 {
				// Check for a possible partial marker at the tail.
				if keep := trailingPrefixLen(chunk, pasteStart); keep > 0 {
					if err := p.write(chunk[:len(chunk)-keep]); err != nil {
						return err
					}
					p.carry.Write(chunk[len(chunk)-keep:])
					return nil
				}
				return p.write(chunk)
			}
			// Forward bytes preceding the marker verbatim.
			if idx > 0 {
				if err := p.write(chunk[:idx]); err != nil {
					return err
				}
			}
			// Consume the marker; it is NOT forwarded yet — we'll forward it
			// together with the paste body after processing.
			chunk = chunk[idx+len(pasteStart):]
			p.inPaste = true
			p.buf.Reset()
			p.buf.Write(pasteStart)
			continue
		}

		// Inside a paste: look for the end marker.
		idx := bytes.Index(chunk, pasteEnd)
		if idx < 0 {
			// Possible partial end marker at the tail.
			if keep := trailingPrefixLen(chunk, pasteEnd); keep > 0 {
				p.buf.Write(chunk[:len(chunk)-keep])
				p.carry.Write(chunk[len(chunk)-keep:])
				// Enforce the cap to avoid unbounded growth from a malicious
				// or broken stream that never sends 201~.
				if p.buf.Len() > pasteMaxBufBytes {
					return p.abortPaste()
				}
				return nil
			}
			p.buf.Write(chunk)
			if p.buf.Len() > pasteMaxBufBytes {
				return p.abortPaste()
			}
			return nil
		}
		// Found the end of the paste.
		p.buf.Write(chunk[:idx])
		p.buf.Write(pasteEnd)
		chunk = chunk[idx+len(pasteEnd):]
		p.inPaste = false
		if err := p.finishPaste(); err != nil {
			return err
		}
	}
	return nil
}

// Close flushes any carry bytes to the underlying writer. Called when the
// ssh session ends.
func (p *pasteInterceptor) Close() error {
	if p.carry.Len() > 0 {
		if err := p.write(p.carry.Bytes()); err != nil {
			return err
		}
		p.carry.Reset()
	}
	if p.inPaste && p.buf.Len() > 0 {
		// Incomplete paste — forward what we have unchanged.
		err := p.write(p.buf.Bytes())
		p.buf.Reset()
		p.inPaste = false
		return err
	}
	return nil
}

// abortPaste is called when a paste body exceeds pasteMaxBufBytes. We flush
// what we have verbatim and return to normal pass-through mode.
func (p *pasteInterceptor) abortPaste() error {
	err := p.write(p.buf.Bytes())
	p.buf.Reset()
	p.inPaste = false
	return err
}

// finishPaste is called after a complete bracketed paste has been buffered.
// It extracts candidate paths, runs upload for each confirmed one, and then
// forwards the raw paste bytes to the WebSocket.
func (p *pasteInterceptor) finishPaste() error {
	body := p.buf.Bytes()
	// Strip the markers to get the inner payload.
	inner := body[len(pasteStart) : len(body)-len(pasteEnd)]

	paths := extractPathCandidates(string(inner))
	for _, raw := range paths {
		abs, info, err := validateSourcePath(raw)
		if err != nil {
			continue
		}
		// Fail open: upload errors do not block the paste forwarding.
		p.doUpload(abs, info.Size()) //nolint:errcheck
	}

	// Flush the paste (markers + original body) to the WebSocket verbatim.
	if err := p.write(body); err != nil {
		return err
	}
	p.buf.Reset()
	return nil
}

// doUpload performs the actual transfer and writes an audit record.
func (p *pasteInterceptor) doUpload(absPath string, size int64) error {
	destDir := filepath.Dir(absPath) + "/"
	if err := p.upload(p.vmName, destDir, absPath, false); err != nil {
		writeAuditLog(p.vmName, absPath, size, false, err.Error())
		return err
	}
	writeAuditLog(p.vmName, absPath, size, true, "")
	return nil
}

// --- helpers ---

// trailingPrefixLen returns the largest n such that buf's last n bytes form a
// proper prefix of marker. Used to defer bytes that might be the start of a
// marker straddling a chunk boundary.
func trailingPrefixLen(buf, marker []byte) int {
	max := len(marker) - 1
	if max > len(buf) {
		max = len(buf)
	}
	for n := max; n > 0; n-- {
		if bytes.HasSuffix(buf, marker[:n]) {
			return n
		}
	}
	return 0
}

// extractPathCandidates returns a deduplicated list of path-like tokens found
// in a pasted text blob.
func extractPathCandidates(s string) []string {
	seen := map[string]bool{}
	var out []string

	// First, pull quoted strings out of s; these may contain spaces. Replace
	// them with spaces in the scan text so pathCandidateRe does not also
	// match their contents as unquoted tokens.
	scan := s
	for _, m := range quotedPathRe.FindAllStringSubmatchIndex(s, -1) {
		var token string
		if m[2] >= 0 {
			token = s[m[2]:m[3]]
		} else if m[4] >= 0 {
			token = s[m[4]:m[5]]
		}
		if looksLikeAbsPath(token) && !seen[token] {
			seen[token] = true
			out = append(out, token)
		}
		scan = scan[:m[0]] + strings.Repeat(" ", m[1]-m[0]) + scan[m[1]:]
	}

	for _, m := range pathCandidateRe.FindAllStringIndex(scan, -1) {
		// Reject match if it is actually a suffix of a larger token (e.g.
		// "./rel.png" would otherwise match "/rel.png"). The character
		// immediately preceding the match must be absent or a non-word
		// boundary character (whitespace, quote, bracket, …).
		if m[0] > 0 {
			prev := scan[m[0]-1]
			if isPathCharBoundary(prev) == false {
				continue
			}
		}
		tok := scan[m[0]:m[1]]
		// Unix shells escape spaces/quotes/backslashes with backslashes.
		// Windows paths use backslash as a separator, so only unescape the
		// specific characters a unix shell would actually escape.
		tok = unescapeShellPath(tok)
		tok = strings.TrimRight(tok, ",;.)]}>'\"")
		if !looksLikeAbsPath(tok) {
			continue
		}
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// isPathCharBoundary reports whether c is a character that can legitimately
// precede an absolute path token (i.e. c is not a character that would make
// the path a suffix of a longer identifier).
func isPathCharBoundary(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '(', '[', '{', '<', '"', '\'', '`', '=', ',', ';', ':':
		return true
	}
	return false
}

// unescapeShellPath replaces `\<X>` with `<X>` for the specific characters
// a POSIX shell would escape in a dragged-and-dropped path: space, single
// quote, double quote, backslash, and a few common metacharacters. It does
// NOT touch `\U` (which would be part of a Windows path like C:\Users).
func unescapeShellPath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case ' ', '\t', '"', '\'', '\\', '(', ')', '[', ']', '{', '}', '*', '?', '$', '&', '|', ';', '<', '>', '#', '!':
				b.WriteByte(next)
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func looksLikeAbsPath(s string) bool {
	if len(s) < 2 {
		return false
	}
	if s[0] == '/' {
		return true
	}
	if strings.HasPrefix(s, "~/") {
		return true
	}
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		return true
	}
	return false
}

// validateSourcePath enforces all source-side security constraints. Returns
// the resolved absolute path and its FileInfo on success.
func validateSourcePath(raw string) (string, os.FileInfo, error) {
	// Expand ~/
	if strings.HasPrefix(raw, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", nil, err
		}
		raw = filepath.Join(home, raw[2:])
	}
	if !filepath.IsAbs(raw) {
		return "", nil, fmt.Errorf("not absolute")
	}
	abs := filepath.Clean(raw)

	// Reject symlinks: use Lstat and refuse non-regular files outright.
	li, err := os.Lstat(abs)
	if err != nil {
		return "", nil, err
	}
	if li.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("symlink refused")
	}
	if !li.Mode().IsRegular() {
		return "", nil, fmt.Errorf("not a regular file")
	}

	// Resolve any parent-component symlinks. EvalSymlinks returns an error
	// for broken links, which is fine — we want to refuse those too.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", nil, err
	}

	// Blocklist: segment match anywhere in EITHER the original path or the
	// symlink-resolved path. This catches cases like ~/Pictures being a
	// symlink to ~/.ssh.
	for _, candidate := range []string{abs, resolved} {
		candSlash := filepath.ToSlash(candidate)
		for _, seg := range sensitivePathSegments {
			segSlash := filepath.ToSlash(seg)
			if strings.Contains(candSlash, "/"+segSlash+"/") ||
				strings.HasSuffix(candSlash, "/"+segSlash) {
				return "", nil, fmt.Errorf("sensitive path segment: %s", seg)
			}
		}
	}
	base := filepath.Base(abs)
	for _, pfx := range sensitiveBasenamePrefixes {
		if base == pfx || strings.HasPrefix(base, pfx) {
			return "", nil, fmt.Errorf("sensitive basename: %s", base)
		}
	}

	// Extension allowlist.
	ext := strings.ToLower(filepath.Ext(abs))
	if !allowedAutoUploadExts[ext] {
		return "", nil, fmt.Errorf("extension not in allowlist: %s", ext)
	}

	// Size limit.
	if li.Size() > maxAutoUploadSize {
		return "", nil, fmt.Errorf("file too large: %d bytes", li.Size())
	}

	return abs, li, nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// writeAuditLog appends a single line to ~/.ib/auto-upload.log recording the
// outcome of an auto-upload attempt. Errors are silently ignored: logging
// must not break the ssh session.
func writeAuditLog(vmName, path string, size int64, ok bool, errMsg string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".ib")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "auto-upload.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	status := "OK"
	if !ok {
		status = "ERR"
	}
	fmt.Fprintf(f, "%s\t%s\t%s\t%d\t%s\t%s\n",
		time.Now().UTC().Format(time.RFC3339),
		vmName,
		status,
		size,
		path,
		strings.ReplaceAll(errMsg, "\n", " "),
	)
}

