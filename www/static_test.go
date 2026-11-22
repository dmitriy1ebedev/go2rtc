package www

import (
    "strings"
    "testing"
)

func TestWebOSPiPEmbedded(t *testing.T) {
    for _, name := range []string{"webos-pip/index.html", "webos-pip/config.js", "webos-pip/camera.js", "webos-pip/mse-player.js"} {
        data, err := Static.ReadFile(name)
        if err != nil || len(data) == 0 { t.Fatalf("embedded %s missing: %v", name, err) }
    }
    data, _ := Static.ReadFile("webos-pip/index.html")
    if !strings.Contains(string(data), "serverAddress") {
        t.Fatal("webOS setup not present in embedded UI")
    }
}
