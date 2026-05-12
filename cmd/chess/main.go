package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pkg/browser"
	"golang.org/x/term"

	"github.com/neoromantics/chess/pkg/api"
	"github.com/neoromantics/chess/pkg/bus"
	"github.com/neoromantics/chess/pkg/db"
	"github.com/neoromantics/chess/pkg/uci"
)

func main() {
	serverMode := flag.Bool("server", false, "force web server mode (default when stdin is a terminal)")
	uciMode := flag.Bool("uci", false, "force UCI mode on stdio (default when stdin is piped)")
	addr := flag.String("addr", "localhost:8080", "Server listen address; falls back to a free port if taken")
	noOpen := flag.Bool("no-open", false, "don't auto-open the browser")
	flag.Parse()

	// Detect if we should run the web server.
	runServer := *serverMode
	if !*serverMode && !*uciMode {
		runServer = stdinIsTerminal()
	}
	if *uciMode {
		runServer = false
	}

	if runServer {
		dsn := os.Getenv("DATABASE_URL")
		if dsn == "" {
			log.Fatal("DATABASE_URL is required (e.g. postgres://chess:pw@chess-db:5432/chess?sslmode=disable)")
		}
		store, err := db.OpenPostgres(dsn)
		if err != nil {
			log.Fatalf("failed to connect to postgres: %v", err)
		}
		defer store.Close()

		redisAddr := os.Getenv("REDIS_URL")
		if redisAddr == "" {
			redisAddr = "localhost:6379"
		}
		eventBus := bus.NewClient(redisAddr)
		defer eventBus.Close()

		ln, err := listenWithFallback(*addr)
		if err != nil {
			log.Fatal(err)
		}
		url := "http://" + ln.Addr().String()
		fmt.Fprintf(os.Stderr, "Chess Platform Server: %s\n", url)

		server := api.NewServer(store, eventBus)

		httpServer := &http.Server{Handler: server}

		// Graceful shutdown on SIGINT/SIGTERM
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigChan
			fmt.Fprintln(os.Stderr, "\nShutting down gracefully...")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			server.Shutdown(ctx)
			httpServer.Shutdown(ctx)
		}()

		if !*noOpen {
			if err := browser.OpenURL(url); err != nil {
				fmt.Fprintf(os.Stderr, "could not open browser: %v\nVisit %s manually.\n", err, url)
			}
		}
		log.Fatal(httpServer.Serve(ln))
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
	return term.IsTerminal(int(os.Stdin.Fd()))
}
