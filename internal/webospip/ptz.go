package webospip

// Native PTZ control for a configured ONVIF camera. This endpoint is opt-in,
// uses no Home Assistant services and never sends camera credentials to the TV.
import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/api"
	"github.com/AlexxIT/go2rtc/internal/streams"
	"github.com/AlexxIT/go2rtc/pkg/onvif"
)

type PTZConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Camera    string `yaml:"camera"`
	Token     string `yaml:"token"`
	ONVIFPort int    `yaml:"onvif_port"`
	StepMS    int    `yaml:"step_ms"`
}

type ptzController struct {
	cfg         PTZConfig
	mu          sync.Mutex
	last        time.Time
	move        func(string) error
	onvifClient *onvif.Client // guarded by mu, never shared across cameras
	onvifToken  string
	onvifUntil  time.Time
}

func registerPTZ(cfg PTZConfig) bool {
	// A native endpoint must never become an unauthenticated LAN motor API.
	if !cfg.Enabled || len(cfg.Token) < 32 || !validName(cfg.Camera) ||
		isTechnical(cfg.Camera, []string{"_pip_mjpeg", "_tv_aac"}) {
		return false
	}
	if cfg.ONVIFPort == 0 {
		cfg.ONVIFPort = 2020 // TP-Link Tapo camera ONVIF service
	}
	if cfg.ONVIFPort < 1024 || cfg.ONVIFPort > 65535 {
		return false
	}
	if cfg.StepMS == 0 {
		cfg.StepMS = 260
	}
	if cfg.StepMS < 100 || cfg.StepMS > 450 {
		return false
	}
	controller := &ptzController{cfg: cfg}
	controller.move = controller.moveONVIF
	api.HandleFunc("api/webos-pip/ptz", controller.handler)
	return true
}

// Resolve camera identity exclusively from the existing go2rtc streams
// registry. Never accept a host, URL, username or password from the client.
// Resolve a configured local DNS hostname into its pinned private IPv4 before
// dialing: the HTTP client never performs a second, potentially unsafe lookup.
func privateONVIFIPv4(host string, lookup func(string) ([]net.IP, error)) (string, error) {
	allowed := func(ip net.IP) bool {
		return ip != nil && ip.To4() != nil && ip.IsPrivate() &&
			!ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsMulticast() &&
			!ip.IsUnspecified()
	}
	if ip := net.ParseIP(host); ip != nil {
		if allowed(ip) {
			return ip.To4().String(), nil
		}
		return "", errors.New("ONVIF address is not a private IPv4")
	}
	name := strings.ToLower(host)
	if len(name) > 253 || !(strings.HasSuffix(name, ".lan") ||
		strings.HasSuffix(name, ".home.arpa")) {
		return "", errors.New("ONVIF camera hostname must be local")
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", errors.New("invalid ONVIF camera hostname")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", errors.New("invalid ONVIF camera hostname")
			}
		}
	}
	addresses, err := lookup(name)
	if err != nil {
		return "", errors.New("local ONVIF DNS lookup failed")
	}
	chosen := ""
	for _, address := range addresses {
		ipv4 := address.To4()
		if ipv4 == nil {
			continue
		}
		if !allowed(ipv4) {
			return "", errors.New("ONVIF DNS returned non-private IPv4")
		}
		if chosen != "" && chosen != ipv4.String() {
			return "", errors.New("ONVIF DNS returned ambiguous IPv4")
		}
		chosen = ipv4.String()
	}
	if chosen == "" {
		return "", errors.New("ONVIF DNS has no private IPv4")
	}
	return chosen, nil
}

func cameraONVIFURL(camera string, port int) (string, error) {
	sources := streams.GetAllSources()[camera]
	for _, source := range sources {
		u, err := url.Parse(source)
		if err != nil || (u.Scheme != "rtsp" && u.Scheme != "onvif") ||
			u.User == nil {
			continue
		}
		hostIP, err := privateONVIFIPv4(u.Hostname(), net.LookupIP)
		if err != nil {
			continue
		}
		if _, ok := u.User.Password(); !ok {
			continue
		}
		return (&url.URL{
			Scheme: "onvif", User: u.User,
			Host: net.JoinHostPort(hostIP, strconv.Itoa(port)),
		}).String(), nil
	}
	return "", errors.New("PTZ source must be a configured private ONVIF/RTSP camera")
}

func (p *ptzController) moveONVIF(direction string) error {
	// The handler serializes access under mu. The camera profile is stable
	// across ordinary PTZ movements; reuse for ten minutes, and invalidate
	// on motor/API error. Repeated capability discovery adds multiple SOAP
	// round trips and makes an otherwise responsive motor feel frozen.
	if p.onvifClient == nil || p.onvifToken == "" || time.Now().After(p.onvifUntil) {
		endpoint, err := cameraONVIFURL(p.cfg.Camera, p.cfg.ONVIFPort)
		if err != nil {
			return err
		}
		client, err := onvif.NewClientWithTimeout(endpoint, 5*time.Second)
		if err != nil {
			return errors.New("ONVIF PTZ camera unavailable")
		}
		tokens, err := client.GetProfilesTokens()
		if err != nil || len(tokens) == 0 {
			return errors.New("ONVIF PTZ profile unavailable")
		}
		p.onvifClient, p.onvifToken, p.onvifUntil =
			client, tokens[0], time.Now().Add(10*time.Minute)
	}
	var err error
	switch direction {
	case "stop":
		err = p.onvifClient.PTZStop(p.onvifToken)
	case "left", "right", "up", "down":
		var x, y float64
		switch direction {
		case "left":
			x = -0.25
		case "right":
			x = 0.25
		case "up":
			y = 0.25
		case "down":
			y = -0.25
		}
		err = p.onvifClient.PTZStep(p.onvifToken, x, y,
			time.Duration(p.cfg.StepMS)*time.Millisecond)
	default:
		return errors.New("invalid PTZ direction")
	}
	if err != nil {
		p.onvifClient, p.onvifToken = nil, ""
	}
	return err
}

// Packaged LG apps have platform-assigned file:// origins: some versions use
// "null", others "file://<app-id>" or "file://<app-id>-webos".
// Allow only this exact app (and the legacy bare file:// origin) here.
// The PTZ credential is mandatory for every POST regardless of Origin.
// This is NOT a permissive, global go2rtc CORS configuration.
func allowedWebOSPTZOrigin(origin string) bool {
	switch origin {
	case "", "null", "file://",
		"file://io.go2rtc.webos.pip",
		"file://io.go2rtc.webos.pip-webos":
		return true
	default:
		return false
	}
}

// readPTZDirection accepts exactly one small JSON object with one
// "direction" field. Duplicate keys, trailing data and partial bodies fail
// closed before any camera operation is queued.
func readPTZDirection(body io.Reader) (string, error) {
	const maxBytes = 160
	data, err := io.ReadAll(io.LimitReader(body, maxBytes+1))
	if err != nil || len(data) > maxBytes {
		return "", errors.New("invalid PTZ body size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return "", errors.New("invalid PTZ object")
	}
	key, err := decoder.Token()
	if err != nil || key != "direction" {
		return "", errors.New("invalid PTZ field")
	}
	var direction string
	if err := decoder.Decode(&direction); err != nil {
		return "", errors.New("invalid PTZ direction")
	}
	closeToken, err := decoder.Token()
	if err != nil || closeToken != json.Delim('}') {
		return "", errors.New("unexpected PTZ field")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", errors.New("trailing PTZ JSON")
	}
	switch direction {
	case "up", "down", "left", "right", "stop":
		return direction, nil
	default:
		return "", errors.New("unsupported PTZ direction")
	}
}

func (p *ptzController) handler(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if !allowedWebOSPTZOrigin(origin) {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Go2RTC-PTZ")
		w.Header().Set("Access-Control-Expose-Headers", "X-Go2RTC-PTZ-Duration-Ms")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if subtle.ConstantTimeCompare(
		[]byte(r.Header.Get("X-Go2RTC-PTZ")), []byte(p.cfg.Token)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	mediaType, params, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaErr != nil || mediaType != "application/json" ||
		(params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		http.Error(w, "expected JSON", http.StatusUnsupportedMediaType)
		return
	}
	direction, err := readPTZDirection(r.Body)
	if err != nil {
		http.Error(w, "invalid PTZ command", http.StatusBadRequest)
		return
	}

	start := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	queueMS := time.Since(start).Milliseconds()
	if direction != "stop" && time.Since(p.last) < 650*time.Millisecond {
		http.Error(w, "PTZ cooldown", http.StatusTooManyRequests)
		return
	}
	cacheHit := p.onvifClient != nil && p.onvifToken != "" &&
		time.Now().Before(p.onvifUntil)
	p.last = time.Now()
	// Log timings without camera URL, credentials or request headers.
	// The log permits separating TV/network wait from ONVIF motor latency.
	err = p.move(direction)
	elapsedMS := time.Since(start).Milliseconds()
	w.Header().Set("X-Go2RTC-PTZ-Duration-Ms", strconv.FormatInt(elapsedMS, 10))
	if err != nil {
		log.Printf("webos-pip-ptz direction=%s result=error duration_ms=%d queue_ms=%d cached=%t",
			direction, elapsedMS, queueMS, cacheHit)
		http.Error(w, "PTZ device unavailable", http.StatusBadGateway)
		return
	}
	log.Printf("webos-pip-ptz direction=%s result=ok duration_ms=%d queue_ms=%d cached=%t",
		direction, elapsedMS, queueMS, cacheHit)
	api.ResponseJSON(w, map[string]bool{"ok": true})
}
