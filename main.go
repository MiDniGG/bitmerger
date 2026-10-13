// BitMerger finds and merges duplicate entries in a Bitwarden or Vaultwarden
// vault. It runs locally and opens its interface in the browser.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/MiDniGG/bitmerger/internal/demo"
	"github.com/MiDniGG/bitmerger/internal/server"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func main() {
	demoMode := flag.Bool("demo", false, "try BitMerger with made-up data, no vault needed")
	noBrowser := flag.Bool("no-browser", false, "do not open the browser, only print the link")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("BitMerger", version)
		return
	}

	var newBackend func() (server.Backend, error)
	if *demoMode {
		newBackend = func() (server.Backend, error) { return demo.New(400 * time.Millisecond), nil }
	}
	srv, listener, err := server.New(version, newBackend)
	if err != nil {
		fmt.Fprintln(os.Stderr, "BitMerger:", err)
		os.Exit(1)
	}

	url := srv.StartURL()
	fmt.Printf("BitMerger %s\n\n", version)
	if *demoMode {
		fmt.Println("Demo mode: made-up data, nothing is changed anywhere.")
	}
	fmt.Println("Open this link in your browser (it only works on this computer):")
	fmt.Printf("\n  %s\n\n", url)
	fmt.Println("Keep this window open while you use BitMerger. Quit in the browser or press Ctrl+C.")
	if !*noBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Println("Could not open the browser automatically, please copy the link.")
		}
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		if srv.JobRunning() {
			fmt.Println("\nStopping after the current group, please wait ...")
		}
		srv.Shutdown()
	}()

	if err := srv.Serve(listener); err != nil {
		fmt.Fprintln(os.Stderr, "BitMerger:", err)
		os.Exit(1)
	}
	<-srv.Done()
	fmt.Println("Logged out, local session data removed. Bye.")
}
