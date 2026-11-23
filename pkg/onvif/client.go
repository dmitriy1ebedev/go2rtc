package onvif

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const PathDevice = "/onvif/device_service"

type Client struct {
	requestTimeout time.Duration
	url            *url.URL

	deviceURL string
	mediaURL  string
	imaginURL string
	ptzURL    string
}

func NewClient(rawURL string) (*Client, error) {
	// Retain upstream compatibility for non-PTZ callers.
	return NewClientWithTimeout(rawURL, 5000*time.Second)
}

// NewClientWithTimeout bounds the initial capabilities/profile requests.
// PTZ calls must not inherit the legacy 5000-second ONVIF HTTP timeout.
func NewClientWithTimeout(rawURL string, requestTimeout time.Duration) (*Client, error) {
	if requestTimeout <= 0 || requestTimeout > 5000*time.Second {
		return nil, errors.New("onvif: invalid request timeout")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	baseURL := "http://" + u.Host

	client := &Client{url: u, requestTimeout: requestTimeout}
	client.deviceURL = baseURL + GetPath(u.Path, PathDevice)

	b, err := client.DeviceRequest(DeviceGetCapabilities)
	if err != nil {
		return nil, err
	}

	s := FindTagValue(b, "Media.+?XAddr")
	client.mediaURL = baseURL + GetPath(s, "/onvif/media_service")

	s = FindTagValue(b, "Imaging.+?XAddr")
	client.imaginURL = baseURL + GetPath(s, "/onvif/imaging_service")

	// PTZ is optional. Do not invent a service URL on a camera that does not
	// advertise it, and retain the original camera host for all SOAP calls.
	if s = FindTagValue(b, "PTZ.+?XAddr"); s != "" {
		client.ptzURL = baseURL + GetPath(s, "/onvif/ptz_service")
	}

	return client, nil
}

func (c *Client) GetURI() (string, error) {
	query := c.url.Query()

	token := query.Get("subtype")

	// support empty
	if i := atoi(token); i >= 0 {
		tokens, err := c.GetProfilesTokens()
		if err != nil {
			return "", err
		}
		if i >= len(tokens) {
			return "", errors.New("onvif: wrong subtype")
		}
		token = tokens[i]
	}

	getUri := c.GetStreamUri
	if query.Has("snapshot") {
		getUri = c.GetSnapshotUri
	}

	b, err := getUri(token)
	if err != nil {
		return "", err
	}

	rawURL := FindTagValue(b, "Uri")
	rawURL = strings.TrimSpace(html.UnescapeString(rawURL))

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}

	if u.User == nil && c.url.User != nil {
		u.User = c.url.User
	}

	return u.String(), nil
}

func (c *Client) GetName() (string, error) {
	b, err := c.DeviceRequest(DeviceGetDeviceInformation)
	if err != nil {
		return "", err
	}

	return FindTagValue(b, "Manufacturer") + " " + FindTagValue(b, "Model"), nil
}

func (c *Client) GetProfilesTokens() ([]string, error) {
	b, err := c.MediaRequest(MediaGetProfiles)
	if err != nil {
		return nil, err
	}

	var tokens []string

	re := regexp.MustCompile(`Profiles.+?token="([^"]+)`)
	for _, s := range re.FindAllStringSubmatch(string(b), 10) {
		tokens = append(tokens, s[1])
	}

	return tokens, nil
}

func (c *Client) HasSnapshots() bool {
	b, err := c.GetServiceCapabilities()
	if err != nil {
		return false
	}
	return strings.Contains(string(b), `SnapshotUri="true"`)
}

func (c *Client) GetProfile(token string) ([]byte, error) {
	return c.Request(
		c.mediaURL, `<trt:GetProfile><trt:ProfileToken>`+token+`</trt:ProfileToken></trt:GetProfile>`,
	)
}

func (c *Client) GetVideoSourceConfiguration(token string) ([]byte, error) {
	return c.Request(c.mediaURL, `<trt:GetVideoSourceConfiguration>
	<trt:ConfigurationToken>`+token+`</trt:ConfigurationToken>
</trt:GetVideoSourceConfiguration>`)
}

func (c *Client) GetStreamUri(token string) ([]byte, error) {
	return c.Request(c.mediaURL, `<trt:GetStreamUri>
	<trt:StreamSetup>
		<tt:Stream>RTP-Unicast</tt:Stream>
		<tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport>
	</trt:StreamSetup>
	<trt:ProfileToken>`+token+`</trt:ProfileToken>
</trt:GetStreamUri>`)
}

func (c *Client) GetSnapshotUri(token string) ([]byte, error) {
	return c.Request(
		c.imaginURL, `<trt:GetSnapshotUri><trt:ProfileToken>`+token+`</trt:ProfileToken></trt:GetSnapshotUri>`,
	)
}

func (c *Client) GetServiceCapabilities() ([]byte, error) {
	// some cameras answer GetServiceCapabilities for media only for path = "/onvif/media"
	return c.Request(
		c.mediaURL, `<trt:GetServiceCapabilities />`,
	)
}

func (c *Client) DeviceRequest(operation string) ([]byte, error) {
	switch operation {
	case DeviceGetServices:
		operation = `<tds:GetServices><tds:IncludeCapability>true</tds:IncludeCapability></tds:GetServices>`
	case DeviceGetCapabilities:
		operation = `<tds:GetCapabilities><tds:Category>All</tds:Category></tds:GetCapabilities>`
	default:
		operation = `<tds:` + operation + `/>`
	}
	return c.Request(c.deviceURL, operation)
}

func (c *Client) MediaRequest(operation string) ([]byte, error) {
	operation = `<trt:` + operation + `/>`
	return c.Request(c.mediaURL, operation)
}

func (c *Client) Request(url, body string) ([]byte, error) {
	if url == "" {
		return nil, errors.New("onvif: unsupported service")
	}

	e := NewEnvelopeWithUser(c.url.User)
	e.Append(body)

	// Keep standard HTTP redirect semantics for poorly implemented ONVIF
	// devices. Upstream explicitly rejected a blanket redirect ban (#2732).
	client := &http.Client{Timeout: c.requestTimeout}
	res, err := client.Post(url, `application/soap+xml;charset=utf-8`, bytes.NewReader(e.Bytes()))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, errors.New("onvif: wrong response " + res.Status)
	}

	return io.ReadAll(res.Body)
}

// PTZRequest is isolated from the legacy media client timeout. Moving a motor
// must never wait minutes for a camera that has lost its connection.
func (c *Client) PTZRequest(body string) ([]byte, error) {
	if c.ptzURL == "" {
		return nil, errors.New("onvif: PTZ service not advertised")
	}
	e := NewEnvelopeWithUser(c.url.User)
	e.Append(body)
	req, err := http.NewRequest(http.MethodPost, c.ptzURL, bytes.NewReader(e.Bytes()))
	if err != nil {
		return nil, errors.New("onvif: cannot create PTZ request")
	}
	req.Header.Set("Content-Type", "application/soap+xml;charset=utf-8")
	// Match upstream redirect behavior; the PTZ-specific timeout stays bounded.
	res, err := (&http.Client{Timeout: 6 * time.Second}).Do(req)
	if err != nil {
		return nil, errors.New("onvif: PTZ request timed out or failed")
	}
	defer res.Body.Close()
	response, err := io.ReadAll(io.LimitReader(res.Body, 256<<10))
	if err != nil {
		return nil, errors.New("onvif: cannot read PTZ response")
	}
	if res.StatusCode != http.StatusOK || bytes.Contains(response, []byte("<s:Fault")) ||
		bytes.Contains(response, []byte("<env:Fault")) {
		return nil, fmt.Errorf("onvif: PTZ request rejected (status %d)", res.StatusCode)
	}
	return response, nil
}

// PTZStop stops a physical camera motor for the given authenticated profile.
func (c *Client) PTZStop(token string) error {
	if token == "" || len(token) > 120 {
		return errors.New("onvif: invalid PTZ profile")
	}
	_, err := c.PTZRequest(
		`<tptz:Stop><tptz:ProfileToken>` + html.EscapeString(token) +
			`</tptz:ProfileToken><tptz:PanTilt>true</tptz:PanTilt><tptz:Zoom>false</tptz:Zoom></tptz:Stop>`)
	return err
}

// PTZStep issues a short bounded movement and ALWAYS attempts Stop afterwards.
// Positive pan goes right and positive tilt goes up (ONVIF normalized units).
// No joystick holding/repeating is permitted by this camera-safe primitive.
func (c *Client) PTZStep(token string, pan, tilt float64, duration time.Duration) error {
	if token == "" || len(token) > 120 || math.IsNaN(pan) || math.IsNaN(tilt) ||
		math.IsInf(pan, 0) || math.IsInf(tilt, 0) ||
		math.Abs(pan) > 0.5 || math.Abs(tilt) > 0.5 ||
		(pan == 0 && tilt == 0) ||
		duration < 100*time.Millisecond || duration > 450*time.Millisecond {
		return errors.New("onvif: PTZ movement outside safe limits")
	}
	profile := html.EscapeString(token)
	movement := fmt.Sprintf(
		`<tptz:ContinuousMove><tptz:ProfileToken>%s</tptz:ProfileToken><tptz:Velocity><tt:PanTilt x="%.3f" y="%.3f"/></tptz:Velocity><tptz:Timeout>PT1S</tptz:Timeout></tptz:ContinuousMove>`,
		profile, pan, tilt)
	if _, err := c.PTZRequest(movement); err != nil {
		// A missing Move acknowledgement does not mean the motor stayed still.
		// Best-effort Stop even when the camera timed out or rejected Move.
		if stopErr := c.PTZStop(token); stopErr != nil {
			return errors.Join(err, fmt.Errorf("onvif: emergency PTZ Stop failed: %w", stopErr))
		}
		return err
	}
	time.Sleep(duration)
	return c.PTZStop(token)
}
