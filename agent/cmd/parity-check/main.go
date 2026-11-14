// Command parity-check checks a running WinBridge server against the agent contract; the key comes from WINBRIDGE_KEY.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/partprogramming09/winbridge/agent/internal/parity"
)

// main prints every deviation from the contract and exits 1 when there is any.
func main() {
	url := flag.String("url", "", "server base URL, for example http://100.x.y.z:8091")
	flag.Parse()
	if *url == "" || os.Getenv("WINBRIDGE_KEY") == "" {
		fmt.Fprintln(os.Stderr, "usage: WINBRIDGE_KEY=... parity-check --url http://host:port")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	fails := parity.Run(ctx, *url, os.Getenv("WINBRIDGE_KEY"))
	for _, f := range fails {
		fmt.Println("FAIL:", f)
	}
	if len(fails) > 0 {
		os.Exit(1)
	}
	fmt.Println("parity: OK")
}
