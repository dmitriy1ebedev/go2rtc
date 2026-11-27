package main

import (
	"context"
	"fmt"
	"os"

	"github.com/AlexxIT/go2rtc/internal/camerabench"
)

// Synthetic-only S2 baseline: FFmpeg testsrc2 and localhost Go HTTP clients.
func main() {
	report, err := camerabench.Run(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "S2 synthetic baseline:", err)
		os.Exit(1)
	}
	data, err := camerabench.JSON(report)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(string(data))
}
