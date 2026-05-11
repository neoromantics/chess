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
	"time"

	"github.com/taiyanliu/chess/pkg/api"
	"github.com/taiyanliu/chess/pkg/db"
	"github.com/taiyanliu/chess/pkg/uci"
)

func main() {
	gui := flag.Bool("gui", false, "force web GUI mode (default when stdin is a terminal)")
	uciMode := flag.Bool("uci", false, "force UCI mode on stdio (default when stdin is piped)")
	addr := flag.String("addr", "localhost:8080", "GUI listen address; falls back to a free port if taken")
	noOpen := flag.Bool("no-open", false, "don't auto-open the browser")
	idleSec := flag.Int("shutdown-on-idle", 0, "exit after N seconds with no /api/ping (0 = never)")
	flag.Parse()

	// Detect if we should run the GUI.
	runGUI := *gui
	if !*gui && !*uciMode {
		runGUI = stdinIsTerminal()
	}
	if *uciMode {
		runGUI = false
	}

	if runGUI {
		var store db.Store
		var err error

		dsn := os.Getenv("DATABASE_URL")
		if dsn != "" {
			store, err = db.OpenPostgres(dsn)
			if err != nil {
				log.Fatalf("failed to connect to postgres: %v", err)
			}
		} else {
			store, err = db.OpenSQLite()
			if err != nil {
				log.Fatal(err)
			}
		}
		defer store.Close()

		ln, err := listenWithFallback(*addr)
		if err != nil {
			log.Fatal(err)
		}
		url := "http://" + ln.Addr().String()
		fmt.Fprintf(os.Stderr, "GUI: %s\n", url)

		idle := time.Duration(*idleSec) * time.Second
		guiSrv := api.NewGUI(store)
		if idle > 0 {
			guiSrv.StartIdleShutdown(idle)
		}

		if !*noOpen {
			openBrowser(url)
		}
		log.Fatal(http.Serve(ln, guiSrv))
	}

	uci.NewUCI(os.Stdout).Run(os.Stdin)
}

// listenWithFallback tries the requested addr; if the port is in use,
// retries on a kernel-assigned free port (host kept).
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
