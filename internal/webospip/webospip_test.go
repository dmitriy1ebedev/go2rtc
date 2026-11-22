package webospip

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCatalog(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(normalizeConfig(Config{RefreshMS: 240}), func() []string {
		return []string{
			"cam_b", "rtsp://admin:secret@host/live", "cam_a",
		}
	})(rec, httptest.NewRequest(http.MethodGet, "/api/webos-pip", nil))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "secret") {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var v struct {
		Cameras []camera `json:"cameras"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Cameras) != 2 || v.Cameras[0].Stream != "cam_a" {
		t.Fatal(v.Cameras)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("no-store")
	}
}
func TestInvalid(t *testing.T) {
	rec := httptest.NewRecorder()
	handler(Config{}, func() []string { return nil })(rec, httptest.NewRequest("POST", "/api/webos-pip", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatal(rec.Code)
	}
	if validName("bad/name") || validName("user@server") || validName("line\nbreak") || !validName("cam1") {
		t.Fatal("stream names")
	}
	c := normalizeConfig(Config{RefreshMS: 1, CatalogPollMS: 1, SoundEnabled: true, SoundStream: "foo/bar"})
	if c.RefreshMS != 240 || c.CatalogPollMS != 15000 || c.SoundEnabled {
		t.Fatal(c)
	}
}

func TestTechnicalStreamFilteredAndDynamic(t *testing.T) {
    names := []string{"cam_b", "cam_b_tv_aac", "cam_a_pip_mjpeg", "cam_a"}
    apiHandler := handler(Config{TechnicalSuffixes: []string{"_tv_aac", "_pip_mjpeg"}}, func() []string {
        return append([]string(nil), names...)
    })
    fetch := func() []camera {
        t.Helper()
        rec := httptest.NewRecorder()
        apiHandler(rec, httptest.NewRequest(http.MethodGet, "/api/webos-pip", nil))
        if rec.Code != 200 { t.Fatalf("unexpected status %d", rec.Code) }
        var result struct { Cameras []camera `json:"cameras"` }
        if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil { t.Fatal(err) }
        return result.Cameras
    }
    before := fetch()
    if len(before) != 2 || before[0].Stream != "cam_a" || before[1].Stream != "cam_b" {
        t.Fatalf("technical streams leaked: %+v", before)
    }
    if before[0].MJPEGStream != "cam_a_pip_mjpeg" || before[1].MJPEGStream != "" {
        t.Fatalf("physical camera -> existing JPEG source mapping is wrong: %+v", before)
    }
    names = []string{"cam_c", "cam_c_tv_aac"}
    after := fetch()
    if len(after) != 1 || after[0].Stream != "cam_c" {
        t.Fatalf("catalog did not dynamically update: %+v", after)
    }
}
func TestOptionalMaximumQualityStream(t *testing.T) {
    names := []string{"door", "door_pip_mjpeg", "door_pip_max_mjpeg", "door_tv_aac"}
    h := handler(Config{TechnicalSuffixes: []string{"_pip_mjpeg", "_tv_aac"}}, func() []string { return names })
    rec := httptest.NewRecorder()
    h(rec, httptest.NewRequest(http.MethodGet, "/api/webos-pip", nil))
    var got catalog
    if err:=json.Unmarshal(rec.Body.Bytes(), &got);err!=nil{t.Fatal(err)}
    if len(got.Cameras)!=1||got.Cameras[0].MJPEGStream!="door_pip_mjpeg"||got.Cameras[0].MJPEGMaxStream!="door_pip_max_mjpeg" {
       t.Fatalf("optional max profile or technical filtering broken: %+v", got.Cameras)
    }
    names=[]string{"door", "door_pip_mjpeg"}
    got=catalog{} // Clear fields omitted by the second JSON response.
    rec=httptest.NewRecorder()
    h(rec, httptest.NewRequest(http.MethodGet, "/api/webos-pip", nil))
    if err:=json.Unmarshal(rec.Body.Bytes(), &got);err!=nil{t.Fatal(err)}
    if len(got.Cameras)!=1||got.Cameras[0].MJPEGMaxStream!="" {t.Fatalf("stale max source advertised: %+v",got.Cameras)}
}

func TestConfigAndNameValidation(t *testing.T) {
    c := normalizeConfig(Config{RefreshMS: -9, CatalogPollMS: 3, TechnicalSuffixes: []string{"../malicious", "_helper"}, SoundStream: "bad/stream", SoundEnabled: true})
    if c.RefreshMS != 240 || c.CatalogPollMS != 15000 || c.SoundEnabled {
        t.Fatalf("unsafe settings accepted: %+v", c)
    }
    if len(c.TechnicalSuffixes) != 1 || c.TechnicalSuffixes[0] != "_helper" {
        t.Fatalf("unsafe technical suffix accepted: %+v", c.TechnicalSuffixes)
    }
}

func TestRuntimeCapabilitiesAreNotAdvertisedBeforeReady(t *testing.T) {
	base := normalizeConfig(Config{
		PTZ: PTZConfig{Enabled: true, Camera: "door", Token: strings.Repeat("s", 48)},
		SoundEnabled: true, SoundStream: "door",
		AudioStream: "door_tv_aac",
	})
	tests := []struct {
		name         string
		streams      []string
		ptzReady     bool
		rtspReady    bool
		wantPTZ      bool
		wantSound    bool
		wantAudio    string
		wantSoundSrc string
	}{
		{"all ready", []string{"door", "door_tv_aac"}, true, true, true, true, "door_tv_aac", "door"},
		{"PTZ rejected", []string{"door", "door_tv_aac"}, false, true, false, true, "door_tv_aac", "door"},
		{"RTSP not available", []string{"door", "door_tv_aac"}, true, false, true, false, "door_tv_aac", ""},
		{"camera missing", []string{"door_tv_aac"}, true, true, false, false, "door_tv_aac", ""},
		{"audio alias missing", []string{"door"}, true, true, true, true, "", "door"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := make(map[string]bool, len(tt.streams))
			for _, name := range tt.streams { found[name] = true }
			cfg := resolveAdvertisedCapabilities(base,
				func(name string) bool { return found[name] }, tt.ptzReady, tt.rtspReady)
			rec := httptest.NewRecorder()
			handler(cfg, func() []string { return tt.streams })(rec,
				httptest.NewRequest(http.MethodGet, "/api/webos-pip", nil))
			if rec.Code != http.StatusOK { t.Fatalf("status: %d", rec.Code) }
			var got catalog
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil { t.Fatal(err) }
			if got.PTZEnabled != tt.wantPTZ || got.SoundEnabled != tt.wantSound ||
				got.AudioStream != tt.wantAudio || got.SoundStream != tt.wantSoundSrc {
				t.Fatalf("incorrect advertised capabilities: %+v", got)
			}
			if !tt.wantPTZ && got.PTZCamera != "" { t.Fatalf("rejected PTZ camera advertised: %q", got.PTZCamera) }
			if strings.Contains(rec.Body.String(), strings.Repeat("s", 48)) {
				t.Fatal("token exposed in catalog")
			}
		})
	}
}

func TestCapabilityCatalogExposesOnlyConfiguredCameraAndAudioStream(t *testing.T) {
    cfg := Config{
        PTZ: PTZConfig{Enabled:true,Camera:"door",Token:strings.Repeat("T",48)},
        AudioStream:"door_tv_aac",
        SoundEnabled:true,SoundStream:"door",
    }
    rec := httptest.NewRecorder()
    handler(cfg,func() []string{return []string{"door","door_tv_aac","door_pip_mjpeg"}})(
       rec,httptest.NewRequest(http.MethodGet,"/api/webos-pip",nil))
    if rec.Code!=http.StatusOK {t.Fatalf("catalog status: %d",rec.Code)}
    var got catalog
    if err:=json.Unmarshal(rec.Body.Bytes(),&got);err!=nil{t.Fatal(err)}
    if !got.PTZEnabled||got.PTZCamera!="door"||!got.SoundEnabled||got.AudioStream!="door_tv_aac" ||
       len(got.Cameras)!=1 || got.Cameras[0].AudioStream!="door_tv_aac" {
        t.Fatalf("missing device capabilities: %+v",got)
    }
    if strings.Contains(rec.Body.String(),strings.Repeat("T",48)) {
        t.Fatal("PTZ token must not be sent in public catalog")
    }
    unsafe:=normalizeConfig(Config{AudioStream:"door/credentials",PTZ:PTZConfig{Enabled:false}})
    if unsafe.AudioStream!="" {t.Fatal("unsafe audio stream accepted")}
}
