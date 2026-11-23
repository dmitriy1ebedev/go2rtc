// Command ipk validates or builds the deterministic go2rtc webOS PiP package.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AlexxIT/go2rtc/webos/ipk"
)

func main() {
	check := flag.Bool("check", false, "validate source package without writing an IPK")
	root := flag.String("root", ".", "repository source directory")
	out := flag.String("out", "", "IPK output path; defaults to out/webos/<name-version>.ipk")
	flag.Parse()
	if flag.NArg() != 0 {
		exit(fmt.Errorf("unknown positional arguments"))
	}
	epoch, err := ipk.EpochFromEnv()
	if err != nil {
		exit(err)
	}
	blob, err := ipk.Build(*root, epoch)
	if err != nil {
		exit(err)
	}
	proof, err := ipk.Inspect(blob)
	if err != nil {
		exit(err)
	}
	if *check {
		fmt.Printf("VALID app_id=%s version=%s sha256=%s files=%d\n", proof.AppID, proof.Version, proof.SHA256, proof.Files)
		return
	}
	target := *out
	if target == "" {
		target = filepath.Join(*root, "out", "webos", "go2rtc-webos-pip-"+proof.Version+".ipk")
	}
	if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		exit(err)
	}
	if err = os.WriteFile(target, blob, 0644); err != nil {
		exit(err)
	}
	fmt.Printf("BUILT %s bytes=%d sha256=%s\n", target, len(blob), proof.SHA256)
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
