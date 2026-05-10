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
)

func main() {
	gui := flag.Bool("gui", false, "force web GUI mode (default when stdin is a terminal)")
	uciMode := flag.Bool("uci", false, "force UCI mode on stdio (default when stdin is piped)")
	addr := flag.String("addr", "localhost:8080", "GUI listen address")
	noOpen := flag.Bool("no-open", false, "don't auto-open the browser in GUI mode")
	flag.Parse()

	// Auto-detect: terminal => GUI; piped (e.g. Cute Chess) => UCI.
	runGUI := *gui
	if !*gui && !*uciMode {
		runGUI = stdinIsTerminal()
	}
	if *uciMode {
		runGUI = false
	}

	if runGUI {
		ln, err := net.Listen("tcp", *addr)
		if err != nil {
			log.Fatal(err)
		}
		url := "http://" + ln.Addr().String()
		fmt.Fprintf(os.Stderr, "GUI: %s\n", url)
		if !*noOpen {
			openBrowser(url)
		}
		log.Fatal(http.Serve(ln, NewGUI()))
	}

	NewUCI(os.Stdout).Run(os.Stdin)
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
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
