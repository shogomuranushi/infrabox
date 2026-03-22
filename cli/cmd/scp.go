package cmd

import (
	"archive/tar"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var scpCmd = &cobra.Command{
	Use:   "scp <src> <dst>",
	Short: "Transfer files to/from a VM",
	Long: `Transfer files between local and a VM via the API.
Use "vmname:path" format to specify a remote path.

Examples:
  ib scp ./local.txt myvm:/tmp/          # local -> VM
  ib scp myvm:/tmp/remote.txt ./         # VM -> local
  ib scp -r ./dir myvm:/home/ubuntu/     # recursive copy`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		mustConfig()
		recursive, _ := cmd.Flags().GetBool("recursive")

		src := args[0]
		dst := args[1]

		srcVM, srcPath := parseVMPath(src)
		dstVM, dstPath := parseVMPath(dst)

		if srcVM != "" && dstVM != "" {
			fmt.Fprintln(os.Stderr, "ERROR: cannot copy between two VMs directly")
			os.Exit(1)
		}
		if srcVM == "" && dstVM == "" {
			fmt.Fprintln(os.Stderr, "ERROR: one of src/dst must be a VM path (vmname:path)")
			os.Exit(1)
		}

		if dstVM != "" {
			// Upload: local -> VM
			if err := uploadToVM(dstVM, dstPath, srcPath, recursive); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Upload complete.")
		} else {
			// Download: VM -> local
			if err := downloadFromVM(srcVM, srcPath, dstPath); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("Download complete.")
		}
	},
}

func parseVMPath(path string) (vm, remotePath string) {
	if strings.Contains(path, "@") {
		return "", path
	}
	idx := strings.Index(path, ":")
	if idx <= 0 {
		return "", path
	}
	return path[:idx], path[idx+1:]
}

func uploadToVM(vmName, destPath, localPath string, recursive bool) error {
	pr, pw := io.Pipe()

	go func() {
		tw := tar.NewWriter(pw)
		defer func() {
			tw.Close()
			pw.Close()
		}()

		info, err := os.Stat(localPath)
		if err != nil {
			pw.CloseWithError(err)
			return
		}

		if info.IsDir() {
			if !recursive {
				pw.CloseWithError(fmt.Errorf("use -r to copy directories"))
				return
			}
			filepath.Walk(localPath, func(path string, fi os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				relPath, _ := filepath.Rel(filepath.Dir(localPath), path)
				if fi.IsDir() {
					tw.WriteHeader(&tar.Header{
						Name:     relPath + "/",
						Typeflag: tar.TypeDir,
						Mode:     int64(fi.Mode()),
					})
					return nil
				}
				tw.WriteHeader(&tar.Header{
					Name: relPath,
					Size: fi.Size(),
					Mode: int64(fi.Mode()),
				})
				f, err := os.Open(path)
				if err != nil {
					return err
				}
				defer f.Close()
				io.Copy(tw, f)
				return nil
			})
		} else {
			tw.WriteHeader(&tar.Header{
				Name: filepath.Base(localPath),
				Size: info.Size(),
				Mode: int64(info.Mode()),
			})
			f, err := os.Open(localPath)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			defer f.Close()
			io.Copy(tw, f)
		}
	}()

	reqURL := fmt.Sprintf("%s/v1/vms/%s/files?path=%s", cfg.Endpoint, url.PathEscape(vmName), url.QueryEscape(destPath))
	req, err := http.NewRequest("POST", reqURL, pr)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", cfg.APIKey)
	req.Header.Set("Content-Type", "application/x-tar")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed (status %d): %s", resp.StatusCode, string(body))
	}
	return nil
}

func downloadFromVM(vmName, srcPath, localDst string) error {
	reqURL := fmt.Sprintf("%s/v1/vms/%s/files?path=%s", cfg.Endpoint, url.PathEscape(vmName), url.QueryEscape(srcPath))
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("download failed (status %d): %s", resp.StatusCode, string(body))
	}

	tr := tar.NewReader(resp.Body)
	absDst, _ := filepath.Abs(localDst)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read error: %w", err)
		}

		// Sanitize: reject paths that escape the destination directory
		cleanName := filepath.Clean(header.Name)
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return fmt.Errorf("invalid tar entry path: %s", header.Name)
		}
		target := filepath.Join(absDst, cleanName)
		if !strings.HasPrefix(target, absDst) {
			return fmt.Errorf("tar entry escapes destination: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, os.FileMode(header.Mode))
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			io.Copy(f, tr)
			f.Close()
		}
	}
	return nil
}

func init() {
	scpCmd.Flags().BoolP("recursive", "r", false, "Recursively copy directories")
}
