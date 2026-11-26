// fork-sbom emits an SPDX 2.3 inventory of Go modules linked into a Linux
// release binary. It does not scan external runtime dependencies (FFmpeg,
// OS libraries or firmware), nor infer their licenses.
package main

import (
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime/debug"
	"sort"
	"time"
)

type checksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"checksumValue"`
}
type pkg struct {
	ID               string     `json:"SPDXID"`
	Name             string     `json:"name"`
	Version          string     `json:"versionInfo,omitempty"`
	Download         string     `json:"downloadLocation"`
	FilesAnalyzed    bool       `json:"filesAnalyzed"`
	LicenseConcluded string     `json:"licenseConcluded"`
	LicenseDeclared  string     `json:"licenseDeclared"`
	Copyright        string     `json:"copyrightText"`
	Checksums        []checksum `json:"checksums,omitempty"`
	Comment          string     `json:"comment,omitempty"`
}
type relationship struct {
	From string `json:"spdxElementId"`
	Type string `json:"relationshipType"`
	To   string `json:"relatedSpdxElement"`
}
type creation struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}
type document struct {
	Version       string         `json:"spdxVersion"`
	DataLicense   string         `json:"dataLicense"`
	ID            string         `json:"SPDXID"`
	Name          string         `json:"name"`
	Namespace     string         `json:"documentNamespace"`
	Creation      creation       `json:"creationInfo"`
	Packages      []pkg          `json:"packages"`
	Relationships []relationship `json:"relationships"`
	Comment       string         `json:"comment"`
}

var sha1RE = regexp.MustCompile("^[0-9a-f]{40}$")
var sha256RE = regexp.MustCompile("^[0-9a-f]{64}$")

func packageEntry(id, name, version string) pkg {
	return pkg{ID: id, Name: name, Version: version, Download: "NOASSERTION", LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION", Copyright: "NOASSERTION"}
}

func inventory(info *debug.BuildInfo, revision, arch, digest string, created time.Time) (document, error) {
	if info == nil || !sha1RE.MatchString(revision) || !sha256RE.MatchString(digest) || created.IsZero() {
		return document{}, errors.New("invalid build identity")
	}
	if arch != "amd64" && arch != "arm64" {
		return document{}, errors.New("unsupported architecture")
	}
	settings := make(map[string]string, len(info.Settings))
	for _, s := range info.Settings {
		settings[s.Key] = s.Value
	}
	if info.Main.Path != "github.com/AlexxIT/go2rtc" || settings["GOOS"] != "linux" || settings["GOARCH"] != arch ||
		settings["CGO_ENABLED"] != "0" || settings["vcs.revision"] != revision || settings["vcs.modified"] != "false" {
		return document{}, errors.New("binary build identity does not match clean pinned Linux source")
	}
	const root = "SPDXRef-Binary"
	d := document{Version: "SPDX-2.3", DataLicense: "CC0-1.0", ID: "SPDXRef-DOCUMENT", Name: "go2rtc-linux-" + arch,
		Namespace: "https://github.com/dmitriy1ebedev/go2rtc/sbom/" + revision + "/" + arch + "/" + digest,
		Creation:  creation{Created: created.UTC().Format(time.RFC3339), Creators: []string{"Tool: go2rtc-fork-sbom"}},
		Comment:   "Linked Go module inventory from embedded binary build information; external FFmpeg, OS dependencies and license conclusions are outside scope."}
	r := packageEntry(root, "go2rtc-linux-"+arch, revision)
	r.Checksums = []checksum{{"SHA256", digest}}
	r.Comment = "Go toolchain " + info.GoVersion + "; module " + info.Main.Path
	d.Packages = append(d.Packages, r)
	d.Relationships = append(d.Relationships, relationship{d.ID, "DESCRIBES", root})
	deps := append([]*debug.Module(nil), info.Deps...)
	for _, m := range deps {
		if m == nil || m.Path == "" || m.Version == "" {
			return document{}, errors.New("invalid linked module")
		}
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Path < deps[j].Path })
	seen := map[string]bool{}
	for _, m := range deps {
		if seen[m.Path] {
			return document{}, errors.New("duplicate module path")
		}
		seen[m.Path] = true
		actual := m
		note := ""
		if m.Replace != nil {
			actual = m.Replace
			if actual.Path == "" || actual.Version == "" {
				return document{}, errors.New("local or unversioned module replacement")
			}
			note = "Replaces " + m.Path + "@" + m.Version + "; "
		}
		idDigest := sha256.Sum256([]byte(m.Path))
		id := "SPDXRef-Module-" + hex.EncodeToString(idDigest[:])
		p := packageEntry(id, actual.Path, actual.Version)
		if actual.Sum != "" {
			note += "Go module checksum: " + actual.Sum
		}
		p.Comment = note
		d.Packages = append(d.Packages, p)
		d.Relationships = append(d.Relationships, relationship{root, "DEPENDS_ON", id})
	}
	return d, nil
}

func inspect(binary, revision, arch string, created time.Time) (document, error) {
	f, err := os.Open(binary)
	if err != nil {
		return document{}, err
	}
	defer f.Close()
	ef, err := elf.NewFile(f)
	if err != nil {
		return document{}, err
	}
	defer ef.Close()
	want := elf.EM_X86_64
	if arch == "arm64" {
		want = elf.EM_AARCH64
	}
	if ef.Class != elf.ELFCLASS64 || ef.Machine != want || ef.Data != elf.ELFDATA2LSB {
		return document{}, errors.New("unexpected ELF architecture")
	}
	info, err := buildinfo.Read(f)
	if err != nil {
		return document{}, err
	}
	h := sha256.New()
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return document{}, err
	}
	if _, err = io.Copy(h, f); err != nil {
		return document{}, err
	}
	return inventory(info, revision, arch, hex.EncodeToString(h.Sum(nil)), created)
}

func run(args []string, w io.Writer) error {
	flags := flag.NewFlagSet("fork-sbom", flag.ContinueOnError)
	binary := flags.String("binary", "", "Linux binary")
	revision := flags.String("revision", "", "Exact source commit")
	arch := flags.String("arch", "", "amd64 or arm64")
	timestamp := flags.String("created", "", "Pinned source commit timestamp (RFC3339)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *binary == "" {
		return errors.New("expected --binary, --revision, --arch and --created")
	}
	created, err := time.Parse(time.RFC3339, *timestamp)
	if err != nil {
		return err
	}
	d, err := inspect(*binary, *revision, *arch, created)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(d)
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
