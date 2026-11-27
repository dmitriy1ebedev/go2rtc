package main

import (
	"encoding/json"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

const revision = "0123456789012345678901234567890123456789"
const digest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func fixture() *debug.BuildInfo {
	return &debug.BuildInfo{GoVersion: "go1.26.0", Main: debug.Module{Path: "github.com/AlexxIT/go2rtc"},
		Settings: []debug.BuildSetting{{Key: "GOOS", Value: "linux"}, {Key: "GOARCH", Value: "arm64"}, {Key: "CGO_ENABLED", Value: "0"}, {Key: "vcs.revision", Value: revision}, {Key: "vcs.modified", Value: "false"}},
		Deps:     []*debug.Module{{Path: "example.org/z", Version: "v1.2.3", Sum: "h1:abc"}, {Path: "example.org/a", Version: "v0.2.0"}}}
}

func TestInventoryDeterministic(t *testing.T) {
	info := fixture()
	stamp := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	a, err := inventory(info, revision, "arm64", digest, stamp)
	if err != nil {
		t.Fatal(err)
	}
	info.Deps[0], info.Deps[1] = info.Deps[1], info.Deps[0]
	b, err := inventory(info, revision, "arm64", digest, stamp)
	if err != nil {
		t.Fatal(err)
	}
	aj, _ := json.Marshal(a)
	bj, _ := json.Marshal(b)
	if string(aj) != string(bj) {
		t.Fatal("non-deterministic inventory")
	}
	if len(a.Packages) != 3 || len(a.Relationships) != 3 || a.Packages[0].Checksums[0].Value != digest || a.Packages[1].Name != "example.org/a" || a.Packages[2].LicenseDeclared != "NOASSERTION" {
		t.Fatalf("unexpected inventory: %+v", a)
	}
}

func TestInventoryRejectsIncorrectIdentity(t *testing.T) {
	stamp := time.Now()
	for _, mutate := range []func(*debug.BuildInfo){
		func(i *debug.BuildInfo) { i.Settings[4].Value = "true" },
		func(i *debug.BuildInfo) { i.Settings[3].Value = strings.Repeat("f", 40) },
		func(i *debug.BuildInfo) { i.Settings[1].Value = "amd64" },
		func(i *debug.BuildInfo) { i.Deps[0].Replace = &debug.Module{Path: "../private"} },
		func(i *debug.BuildInfo) { i.Deps = append(i.Deps, i.Deps[0]) },
	} {
		i := fixture()
		mutate(i)
		if _, err := inventory(i, revision, "arm64", digest, stamp); err == nil {
			t.Fatal("accepted invalid build identity or module")
		}
	}
}
