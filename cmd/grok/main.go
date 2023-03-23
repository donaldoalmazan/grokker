package main

import (
	"os"

	. "github.com/stevegt/goadapt"
	"github.com/stevegt/grokker"

	// "github.com/joho/godotenv"
	// "github.com/docopt/docopt-go"
	"github.com/namsral/flag"
)

var usage = `
Usage:
	grok add <docfn>
	grok [-g] q <question> 

Options:
	-g  Include results from OpenAI's global knowledge base as well as
		from local documents.
`

func main() {
	// parse args using flag package
	global := flag.Bool("g", false, "Include results from OpenAI's global knowledge base as well as from local documents.")
	flag.Parse()
	args := flag.Args()
	cmd := args[0]

	// get the current directory
	dir, err := os.Getwd()
	Ck(err)
	grokfn := dir + "/.grok"

	var grok *grokker.Grokker
	switch cmd {
	case "add":
		if len(args) < 2 {
			Pl("Error: add command requires a filename argument")
			os.Exit(1)
		}
		Pf("Creating .grok file...")
		grok = grokker.New()
		for _, docfn := range args[1:] {
			// add the document
			Pf(" adding %s...", docfn)
			err = grok.AddDocument(docfn)
			Ck(err)
		}
		// save the grok file
		fh, err := os.Create(grokfn)
		err = grok.Save(fh)
		Ck(err)
		Pl(" done!")
	case "q":
		if len(args) < 2 {
			Pl("Error: q command requires a question argument")
			os.Exit(1)
		}
		question := args[len(args)-1]
		// see if there's a .grok file in the current directory
		if _, err := os.Stat(grokfn); err != nil {
			Pl("No .grok file found in current directory.")
			os.Exit(1)
		}
		// load the .grok file
		fh, err := os.Open(grokfn)
		grok, err = grokker.Load(fh)
		Ck(err)

		// answer the question
		resp, query, err := grok.Answer(question, *global)
		Ck(err)
		_ = query
		// Pprint(resp)
		Pl(resp.Choices[0].Message.Content)
	default:
		Pl("Error: unrecognized command")
		os.Exit(1)
	}
}
