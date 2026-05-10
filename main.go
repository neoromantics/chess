package main

import "os"

func main() {
	NewUCI(os.Stdout).Run(os.Stdin)
}
