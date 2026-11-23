package tests

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/AlexxIT/go2rtc/webos/ipk"
)

// These subprocesses are invoked only by "go test" through MCPshka
// project_test; they do not access the network or a real LG device.
func runCheck(t *testing.T, tool string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath(tool); err != nil {
		t.Fatalf("%s required for webOS acceptance tests: %v", tool, err)
	}
	c := exec.Command(tool, args...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s failed: %v\n%s", tool, strings.Join(args, " "), err, out)
	}
	t.Logf("PASS %s %s\n%s", tool, strings.Join(args, " "), string(out))
}

// Fail if future edits accidentally bring an exclusive LG video decoder back.
func TestJPEGOnlyPiPReleaseContract(t *testing.T) {
	html, err := os.ReadFile("../../www/webos-pip/index.html")
	if err != nil {
		t.Fatal(err)
	}
	client, err := os.ReadFile("../../www/webos-pip/camera.js")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "<video") ||
		strings.Contains(string(html), "src=\"mse-player.js\"") {
		t.Fatal("PiP must not create a video element or load the MSE player")
	}
	for _, unsafe := range []string{"attemptMSE", "Go2RTCWebOSMSE", "video.play("} {
		if strings.Contains(string(client), unsafe) {
			t.Fatalf("PiP acquired a video decoder: %s", unsafe)
		}
	}
	for _, safe := range []string{"/api/stream.mjpeg?src=", "/api/frame.jpeg?src=", "fitImage"} {
		if !strings.Contains(string(client), safe) {
			t.Fatalf("missing JPEG-only fallback: %s", safe)
		}
	}
}

func TestJavaScriptAndClient(t *testing.T) {
	runCheck(t, "node", "--check", "../../www/webos-pip/camera.js")
	// The JPEG-only release does not ship the legacy MSE player.
	runCheck(t, "node", "test_client.js")
}

// The test suite is Go-native: archive generation, validation and the
// independent JS client test (via Node) do not execute Python.
func TestGoSecurityAndIPK(t *testing.T) {
	data, err := ipk.Build("../..", 0)
	if err != nil {
		t.Fatal(err)
	}
	proof, err := ipk.Inspect(data)
	if err != nil {
		t.Fatal(err)
	}
	if proof.AppID != ipk.AppID || proof.Files != 10 {
		t.Fatalf("unexpected IPK inspection result: %+v", proof)
	}
}
