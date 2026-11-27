// Package ipk creates and validates deterministic, self-contained LG webOS
// PiP artifacts with only the Go standard library. It never talks to a TV.
package ipk

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const AppID = "io.go2rtc.webos.pip"
const maxArchive = 32 << 20

type Proof struct {
	AppID   string `json:"app_id"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Files   int    `json:"files"`
}

type entry struct {
	name string
	data []byte
	dir  bool
}

func Build(root string, epoch int64) ([]byte, error) {
	var app struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	var versions struct {
		WebOSApp string `json:"webos_app"`
	}
	if err := readJSON(filepath.Join(root, "webos/appinfo.json"), &app); err != nil {
		return nil, err
	}
	if err := readJSON(filepath.Join(root, "versions.json"), &versions); err != nil {
		return nil, err
	}
	if app.ID != AppID {
		return nil, errors.New("wrong webOS app ID")
	}
	valid := regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	if !valid.MatchString(app.Version) || app.Version != versions.WebOSApp {
		return nil, errors.New("invalid or inconsistent webOS app version")
	}
	for _, part := range strings.Split(app.Version, ".") {
		n, err := strconv.ParseUint(part, 10, 64)
		if err != nil || n > 999999999 {
			return nil, errors.New("invalid version component")
		}
	}
	source := [][2]string{
		{"LICENSE", "LICENSE"}, {"NOTICE", "webos/NOTICE"}, {"appinfo.json", "webos/appinfo.json"},
		{"camera.js", "www/webos-pip/camera.js"}, {"config.js", "webos/config.js"},
		{"icon.png", "webos/icon.png"}, {"index.html", "www/webos-pip/index.html"},
		{"largeIcon.png", "webos/largeIcon.png"},
	}
	content := map[string][]byte{}
	total := 0
	for _, pair := range source {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(pair[1])))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pair[1], err)
		}
		if len(b) > 8<<20 {
			return nil, errors.New("application asset too large")
		}
		content[pair[0]] = b
		total += len(b)
	}
	marker := []byte("{{APP_VERSION}}")
	if bytes.Count(content["index.html"], marker) != 2 {
		return nil, errors.New("expected exactly two UI version markers")
	}
	content["index.html"] = bytes.ReplaceAll(content["index.html"], marker, []byte(app.Version))
	if bytes.Contains(content["config.js"], []byte("rtsp://")) || bytes.Contains(content["config.js"], []byte("http://192.")) {
		return nil, errors.New("camera credentials or LAN host in public config")
	}
	if bytes.Contains(content["index.html"], []byte("<video")) || bytes.Contains(content["camera.js"], []byte("attemptMSE")) {
		return nil, errors.New("PiP must not consume a video decoder")
	}
	prefix := "usr/palm/applications/" + AppID
	data := []entry{{name: "usr", dir: true}, {name: "usr/palm", dir: true}, {name: "usr/palm/applications", dir: true}, {name: prefix, dir: true}, {name: "usr/palm/packages", dir: true}, {name: "usr/palm/packages/" + AppID, dir: true}}
	for _, pair := range source {
		data = append(data, entry{name: prefix + "/" + pair[0], data: content[pair[0]]})
	}
	packageInfo, _ := json.Marshal(struct {
		App     string `json:"app"`
		ID      string `json:"id"`
		Version string `json:"version"`
	}{AppID, AppID, app.Version})
	data = append(data, entry{name: "usr/palm/packages/" + AppID + "/packageinfo.json", data: packageInfo})
	control := fmt.Sprintf("Package: %s\nVersion: %s\nSection: misc\nPriority: optional\nArchitecture: all\nInstalled-Size: %d\nMaintainer: go2rtc webOS PiP contributors\nDescription: Standalone go2rtc camera companion for LG webOS\nwebOS-Package-Format-Version: 2\nwebOS-Packager-Version: 1.0.0\n", AppID, app.Version, total)
	ctrl, err := makeTar([]entry{{name: "control", data: []byte(control)}}, epoch)
	if err != nil {
		return nil, err
	}
	payload, err := makeTar(data, epoch)
	if err != nil {
		return nil, err
	}
	members := []entry{{name: "debian-binary", data: []byte("2.0\n")}, {name: "control.tar.gz", data: ctrl}, {name: "data.tar.gz", data: payload}}
	var out bytes.Buffer
	out.WriteString("!<arch>\n")
	for _, m := range members {
		header := fmt.Sprintf("%-16s%-12d%-6d%-6d%-8s%-10d", m.name, 0, 0, 0, "100644", len(m.data))
		if len(header) != 58 {
			return nil, errors.New("invalid ar header")
		}
		out.WriteString(header)
		out.Write([]byte{0x60, 0x0a})
		out.Write(m.data)
		if len(m.data)%2 != 0 {
			out.WriteByte('\n')
		}
	}
	if out.Len() > maxArchive {
		return nil, errors.New("IPK size limit exceeded")
	}
	return out.Bytes(), nil
}

func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func makeTar(entries []entry, epoch int64) ([]byte, error) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	for _, e := range entries {
		name := e.name
		mode := int64(0644)
		typeflag := byte(tar.TypeReg)
		if e.dir {
			mode = 0755
			typeflag = tar.TypeDir
			name += "/"
		}
		h := &tar.Header{Name: name, Mode: mode, Uid: 0, Gid: 0, ModTime: time.Unix(epoch, 0).UTC(), Typeflag: typeflag, Format: tar.FormatUSTAR, Size: int64(len(e.data))}
		if e.dir {
			h.Size = 0
		}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if !e.dir {
			if _, err := tw.Write(e.data); err != nil {
				return nil, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	var zipped bytes.Buffer
	gz, err := gzip.NewWriterLevel(&zipped, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	gz.Header.ModTime = time.Unix(0, 0).UTC()
	if _, err := gz.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return zipped.Bytes(), nil
}

// Inspect rejects malformed archives, unsafe members, extra files and unknown
// applications. It does not extract or execute any member.
func Inspect(blob []byte) (Proof, error) {
	var empty Proof
	if len(blob) < 8 || len(blob) > maxArchive || string(blob[:8]) != "!<arch>\n" {
		return empty, errors.New("invalid IPK magic or length")
	}
	members := map[string][]byte{}
	for off := 8; off < len(blob); {
		if len(blob)-off < 60 || blob[off+58] != 0x60 || blob[off+59] != '\n' {
			return empty, errors.New("invalid ar header")
		}
		h := blob[off : off+60]
		name := strings.TrimSuffix(strings.TrimSpace(string(h[:16])), "/")
		size, err := strconv.Atoi(strings.TrimSpace(string(h[48:58])))
		if err != nil || size < 0 || size > maxArchive {
			return empty, errors.New("invalid ar size")
		}
		off += 60
		if size > len(blob)-off {
			return empty, errors.New("truncated ar member")
		}
		if _, duplicate := members[name]; duplicate {
			return empty, errors.New("duplicate ar member")
		}
		members[name] = blob[off : off+size]
		off += size
		if size%2 != 0 {
			if off >= len(blob) || blob[off] != '\n' {
				return empty, errors.New("bad ar padding")
			}
			off++
		}
	}
	if len(members) != 3 || string(members["debian-binary"]) != "2.0\n" {
		return empty, errors.New("wrong IPK sections or Debian format")
	}
	contents := map[string][]byte{}
	for _, section := range []string{"control.tar.gz", "data.tar.gz"} {
		buf, ok := members[section]
		if !ok {
			return empty, errors.New("missing archive section")
		}
		zr, err := gzip.NewReader(bytes.NewReader(buf))
		if err != nil {
			return empty, err
		}
		tr := tar.NewReader(io.LimitReader(zr, maxArchive))
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				zr.Close()
				return empty, err
			}
			name := strings.TrimSuffix(h.Name, "/")
			if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") || name == "" {
				zr.Close()
				return empty, errors.New("unsafe tar path")
			}
			for _, piece := range strings.Split(name, "/") {
				if piece == ".." || piece == "." || piece == "" {
					zr.Close()
					return empty, errors.New("unsafe tar traversal")
				}
			}
			if h.Uid != 0 || h.Gid != 0 {
				zr.Close()
				return empty, errors.New("invalid owner in IPK")
			}
			if h.Typeflag == tar.TypeDir {
				if h.Mode != 0755 {
					zr.Close()
					return empty, errors.New("invalid directory mode")
				}
				continue
			}
			if h.Typeflag != tar.TypeReg || h.Mode != 0644 || h.Size < 0 || h.Size > 8<<20 {
				zr.Close()
				return empty, errors.New("unsafe tar entry")
			}
			if _, duplicate := contents[name]; duplicate {
				zr.Close()
				return empty, errors.New("duplicate tar entry")
			}
			b, err := io.ReadAll(io.LimitReader(tr, h.Size+1))
			if err != nil || int64(len(b)) != h.Size {
				zr.Close()
				return empty, errors.New("truncated tar entry")
			}
			contents[name] = b
		}
		if err := zr.Close(); err != nil {
			return empty, err
		}
	}
	prefix := "usr/palm/applications/" + AppID + "/"
	for _, name := range []string{"LICENSE", "NOTICE", "appinfo.json", "camera.js", "config.js", "icon.png", "index.html", "largeIcon.png"} {
		if _, ok := contents[prefix+name]; !ok {
			return empty, fmt.Errorf("missing IPK file %s", name)
		}
	}
	pkg := "usr/palm/packages/" + AppID + "/packageinfo.json"
	if _, ok := contents["control"]; !ok {
		return empty, errors.New("missing control")
	}
	if _, ok := contents[pkg]; !ok || len(contents) != 10 {
		return empty, errors.New("wrong packaged file count")
	}
	var meta struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(contents[prefix+"appinfo.json"], &meta); err != nil {
		return empty, err
	}
	if meta.ID != AppID {
		return empty, errors.New("unexpected package ID")
	}
	html := contents[prefix+"index.html"]
	if bytes.Contains(html, []byte("{{APP_VERSION}}")) || bytes.Count(html, []byte("v"+meta.Version)) != 2 {
		return empty, errors.New("UI version differs from package")
	}
	if bytes.Contains(html, []byte("<video")) || bytes.Contains(contents[prefix+"camera.js"], []byte("attemptMSE")) {
		return empty, errors.New("PiP attempted video decoder")
	}
	if bytes.Contains(contents[prefix+"config.js"], []byte("rtsp://")) {
		return empty, errors.New("RTSP credentials in package")
	}
	var packageInfo struct {
		App     string `json:"app"`
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(contents[pkg], &packageInfo); err != nil {
		return empty, err
	}
	if packageInfo.App != AppID || packageInfo.ID != AppID || packageInfo.Version != meta.Version {
		return empty, errors.New("packageinfo mismatch")
	}
	sha := sha256.Sum256(blob)
	return Proof{AppID, meta.Version, hex.EncodeToString(sha[:]), len(contents)}, nil
}

func EpochFromEnv() (int64, error) {
	text := os.Getenv("SOURCE_DATE_EPOCH")
	if text == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(text, 10, 64)
	if err != nil || v < 0 {
		return 0, errors.New("invalid SOURCE_DATE_EPOCH")
	}
	return v, nil
}
