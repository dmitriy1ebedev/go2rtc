package webospip

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/core"
)

// Exercise actual HTTP requests against the same registry used in Init,
// never a device or production configuration.
func TestLiveRegistryCatalogAddRemove(t *testing.T) {
	const prefix = "catalog_fixture_"
	const source = "catalogfixture://test-user:synthetic-secret@invalid.example/video"
	streams.HandleFunc("catalogfixture", func(string) (core.Producer, error) {
		return nil, errors.New("catalog must never dial a source")
	})
	names := []string{prefix+"door", prefix+"door_preview_mjpeg", prefix+"door_tv_aac", prefix+"garden"}
	for _, name := range names {
		if streams.Get(name) != nil { t.Fatal("fixture collision") }
		t.Cleanup(func(){ streams.Delete(name) })
	}
	add := func(name string) {
		t.Helper()
		if _, err := streams.New(name, source); err != nil { t.Fatal(err) }
	}
	for _, name := range names[:3] { add(name) }
	cfg := normalizeConfig(Config{
		TechnicalSuffixes: []string{"_preview_mjpeg", "_tv_aac"},
		PTZ: PTZConfig{Enabled:true, Camera:names[0], Token:strings.Repeat("X",48)},
		SoundEnabled:true, SoundStream:names[0], AudioStream:names[2],
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/webos-pip", handler(cfg, streams.GetAllNames))
	mux.HandleFunc("/api/cameras/capabilities", capabilitiesHandler(cfg, streams.GetAllNames))
	server := httptest.NewServer(mux)
	defer server.Close()
	client := server.Client()
	client.Timeout = 2*time.Second
	get := func(path string, target any) {
		t.Helper()
		r,err := client.Get(server.URL+path)
		if err != nil { t.Fatal(err) }
		defer r.Body.Close()
		body,err := io.ReadAll(io.LimitReader(r.Body,65537))
		if err != nil || len(body)>65536 || r.StatusCode != 200 { t.Fatalf("invalid response: %d %v",r.StatusCode,err) }
		for _, secret := range []string{"synthetic-secret","invalid.example","catalogfixture://",strings.Repeat("X",48)} {
			if strings.Contains(string(body),secret) { t.Fatal("secret leaked") }
		}
		if r.Header.Get("Cache-Control")!="no-store" { t.Fatal("missing cache policy") }
		if err=json.Unmarshal(body,target);err!=nil { t.Fatal(err) }
	}
	var legacy catalog
	var modern capabilitiesResponse
	get("/api/webos-pip",&legacy)
	get("/api/cameras/capabilities",&modern)
	if len(legacy.Cameras)!=1 || legacy.Cameras[0].Stream!=names[0] || legacy.Cameras[0].MJPEGStream!=names[1] || !legacy.PTZEnabled || !legacy.SoundEnabled || legacy.AudioStream!=names[2] { t.Fatalf("unexpected legacy catalog: %+v",legacy) }
	if len(modern.Cameras)!=1 || modern.Cameras[0].ID!=names[0] || modern.Cameras[0].Streams.MJPEG!=names[1] || modern.Cameras[0].Streams.Audio!=names[2] { t.Fatalf("unexpected capability catalog: %+v",modern) }
	// Remove an alias while retaining its camera; no stale metadata survives.
	streams.Delete(names[2])
	legacy=catalog{}
	get("/api/webos-pip",&legacy)
	if legacy.AudioStream!="" || legacy.Cameras[0].AudioStream!="" { t.Fatal("deleted audio alias still advertised") }
	streams.Delete(names[0])
	streams.Delete(names[1])
	add(names[3])
	legacy=catalog{}
	modern=capabilitiesResponse{}
	get("/api/webos-pip",&legacy)
	get("/api/cameras/capabilities",&modern)
	if len(legacy.Cameras)!=1 || legacy.Cameras[0].Stream!=names[3] || legacy.PTZEnabled || legacy.PTZCamera!="" || legacy.SoundEnabled || legacy.SoundStream!="" { t.Fatalf("stale legacy capabilities: %+v",legacy) }
	if len(modern.Cameras)!=1 || modern.Cameras[0].ID!=names[3] || modern.Cameras[0].Features.PTZ || modern.Cameras[0].Features.SoundActivity { t.Fatalf("stale modern capabilities: %+v",modern) }
}
