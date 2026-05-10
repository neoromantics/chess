package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
)

func main() {
	gui := flag.Bool("gui", false, "run web GUI on -addr instead of UCI on stdio")
	addr := flag.String("addr", "localhost:8080", "GUI listen address")
	flag.Parse()

	if *gui {
		fmt.Fprintf(os.Stderr, "GUI: http://%s\n", *addr)
		log.Fatal(http.ListenAndServe(*addr, NewGUI()))
		return
	}

	NewUCI(os.Stdout).Run(os.Stdin)
}
