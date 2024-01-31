package main

import (
	"os"

	. "github.com/stevegt/goadapt"
	"github.com/stevegt/grokker/v3/grokker"
)

// main simply calls the cli package's Cli() function
func main() {
	config := grokker.NewCliConfig()
	rc, err := grokker.Cli(os.Args[1:], config)
	Ck(err)
	os.Exit(rc)
}
