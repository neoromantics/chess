package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func main() {
	gui := flag.Bool("gui", false, "force web GUI mode (default when stdin is a terminal or .app launch)")
	uciMode := flag.Bool("uci", false, "force UCI mode on stdio (default when stdin is piped)")
	addr := flag.String("addr", "localhost:8080", "GUI listen address; falls back to a free port if taken")
	noOpen := flag.Bool("no-open", false, "don't auto-open the browser in GUI mode")
	idleSec := flag.Int("shutdown-on-idle", 0, "exit after N seconds with no /api/ping (0 = never; default 30 in .app launches)")
	flag.Parse()

	app := launchedFromAppBundle()
	// .app double-clicks have no TTY but should still launch the GUI; same
	// goes for explicit -gui.
	runGUI := *gui || app
	if !*gui && !app && !*uciMode {
		runGUI = stdinIsTerminal()
	}
	if *uciMode {
		runGUI = false
	}

	if runGUI {
		ln, err := listenWithFallback(*addr)
		if err != nil {
			log.Fatal(err)
		}
		url := "http://" + ln.Addr().String()
		fmt.Fprintf(os.Stderr, "GUI: %s\n", url)

		idle := time.Duration(*idleSec) * time.Second
		if idle == 0 && app {
			// Sensible default for double-click launches: tab close => exit.
			idle = 30 * time.Second
		}
		guiSrv := NewGUI()
		if idle > 0 {
			guiSrv.startIdleShutdown(idle)
		}

		if !*noOpen {
			openBrowser(url)
		}
		log.Fatal(http.Serve(ln, guiSrv))
	}

	NewUCI(os.Stdout).Run(os.Stdin)
}

// launchedFromAppBundle reports whether the executable lives inside a
// macOS .app bundle (Contents/MacOS/<name>).
func launchedFromAppBundle() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.Contains(exe, ".app/Contents/MacOS/")
}

// listenWithFallback tries the requested addr; if the port is in use,
// retries on a kernel-assigned free port (host kept). Lets a .app double-
// click launch even when an earlier instance is still running.
func listenWithFallback(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, nil
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, err
	}
	fmt.Fprintf(os.Stderr, "%s in use, trying a free port\n", addr)
	return net.Listen("tcp", host+":0")
}

func stdinIsTerminal() bool {
	s, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return s.Mode()&os.ModeCharDevice != 0
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// Absolute path: when launched as an .app, the inherited PATH may
		// not include /usr/bin, and `exec.Command("open")` then fails
		// silently. /usr/bin/open is part of the macOS base install.
		cmd = exec.Command("/usr/bin/open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "could not open browser: %v\nVisit %s manually.\n", err, url)
	}
}
