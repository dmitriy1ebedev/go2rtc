package ipk

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sourceRoot() string { return filepath.Clean(filepath.Join("..", "..")) }

func TestDeterministicBuildAndInspect(t *testing.T) {
	a, err := Build(sourceRoot(), 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(sourceRoot(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("identical source and epoch produced different archive")
	}
	c, err := Build(sourceRoot(), 1234567890)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("SOURCE_DATE_EPOCH must change archive")
	}
	proof, err := Inspect(a)
	if err != nil {
		t.Fatal(err)
	}
	if proof.AppID != AppID || proof.Files != 10 || len(proof.SHA256) != 64 {
		t.Fatalf("wrong package proof: %+v", proof)
	}
	var app struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile(filepath.Join(sourceRoot(), "webos", "appinfo.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &app); err != nil {
		t.Fatal(err)
	}
	if proof.Version != app.Version {
		t.Fatalf("version mismatch %s != %s", proof.Version, app.Version)
	}
}

func TestRejectsCorruptAndTruncatedArchive(t *testing.T) {
	data, err := Build(sourceRoot(), 0)
	if err != nil {
		t.Fatal(err)
	}
	bad := [][]byte{nil, []byte("not an ipk"), []byte("!<arch>\n"), data[:100], data[:len(data)-1]}
	for i, b := range bad {
		if _, err := Inspect(b); err == nil {
			t.Fatalf("corrupt IPK %d accepted", i)
		}
	}
	tampered := append([]byte(nil), data...)
	tampered[8+58] = '?'
	if _, err := Inspect(tampered); err == nil {
		t.Fatal("corrupt ar member accepted")
	}
	if _, err := Inspect(append(data, data...)); err == nil {
		t.Fatal("appended duplicate ar archive accepted")
	}
}

func TestPublishedPackageNoCredentialsOrVideoDecoder(t *testing.T) {
	b, err := Build(sourceRoot(), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{[]byte("tapo_c230"), []byte("192.168.0."), []byte("rtsp://"), []byte("admin:password")} {
		if bytes.Contains(b, bad) {
			t.Fatalf("private marker %q in public IPK", bad)
		}
	}
	html, err := os.ReadFile(filepath.Join(sourceRoot(), "www", "webos-pip", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := os.ReadFile(filepath.Join(sourceRoot(), "www", "webos-pip", "camera.js"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(html, []byte("<video")) || bytes.Contains(client, []byte("attemptMSE")) {
		t.Fatal("a video decoder was introduced")
	}
	notice, err := os.ReadFile(filepath.Join(sourceRoot(), "webos", "NOTICE"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(notice, []byte("Alexey Khit")) {
		t.Fatal("upstream attribution missing")
	}
}

func TestVersionAndReleaseMetadata(t *testing.T) {
	var versions struct {
		Schema      string `json:"schema"`
		ForkRelease string `json:"fork_release"`
		ForkGitTag  string `json:"fork_git_tag"`
		WebOSApp    string `json:"webos_app"`
		Upstream    string `json:"upstream"`
	}
	if err := readJSON(filepath.Join(sourceRoot(), "versions.json"), &versions); err != nil {
		t.Fatal(err)
	}
	if versions.Schema != "go2rtc-webos/versions-v1" || versions.ForkGitTag != "lg-webos-v"+versions.ForkRelease || versions.Upstream == "go2rtc-webos" {
		t.Fatalf("inconsistent version manifest: %+v", versions)
	}
	var app struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	}
	if err := readJSON(filepath.Join(sourceRoot(), "webos", "appinfo.json"), &app); err != nil {
		t.Fatal(err)
	}
	if app.Version != versions.WebOSApp || app.ID != AppID {
		t.Fatal("app and release versions differ")
	}
	profile, err := os.ReadFile(filepath.Join(sourceRoot(), ".mcpshka", "profile.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(profile), "go2rtc-webos-pip-"+app.Version+".ipk") {
		t.Fatal("profile artifact version is stale")
	}
}

func TestEpochParsing(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "")
	if v, err := EpochFromEnv(); err != nil || v != 0 {
		t.Fatal(v, err)
	}
	t.Setenv("SOURCE_DATE_EPOCH", "1234")
	if v, err := EpochFromEnv(); err != nil || v != 1234 {
		t.Fatal(v, err)
	}
	for _, bad := range []string{"-1", "NaN", "999999999999999999999999"} {
		t.Setenv("SOURCE_DATE_EPOCH", bad)
		if _, err := EpochFromEnv(); err == nil {
			t.Fatalf("accepted invalid epoch %q", bad)
		}
	}
}
