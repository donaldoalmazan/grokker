package aidda

import (
	"bufio"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"
	"github.com/stevegt/envi"
	. "github.com/stevegt/goadapt"
	"github.com/stevegt/grokker/v3/core"
	"github.com/stevegt/grokker/v3/util"
)

/*
XXX update this
- while true
	- git commit
	- present user with an editor buffer where they can type a natural language instruction
	- send that along with all files to GPT API
		- filter out files using .aidda/ignore
	- save returned files over top of the existing files
	- run 'git difftool' with vscode as in https://www.roboleary.net/vscode/2020/09/15/vscode-git.html
	- open diff tool in editor so user can selectively choose and edit changes
	- run go test -v
	- include test results in the .aidda/test file
*/

var (
	baseDir     string
	ignoreFn    string
	commitMsgFn string
)

var DefaultSysmsg = "You are an expert Go programmer. Please make the requested changes to the given code or documentation."

func Do(g *core.Grokker, args ...string) (err error) {
	defer Return(&err)

	baseDir = g.Root

	// ensure we're in a git repository
	// XXX location might want to be more flexible
	_, err = os.Stat(Spf("%s/.git", baseDir))
	Ck(err)

	// create a directory for aidda files
	// XXX location might want to be more flexible
	dir := Spf("%s/.aidda", baseDir)
	err = os.MkdirAll(dir, 0755)
	Ck(err)

	// generate filenames
	// XXX these should all be in a struct
	promptFn := Spf("%s/prompt", dir)
	ignoreFn = Spf("%s/ignore", dir)
	testFn := Spf("%s/test", dir)
	commitMsgFn = Spf("%s/commitmsg", dir)

	// Ensure there is an ignore file
	err = ensureIgnoreFile(ignoreFn)
	Ck(err)

	// Create the prompt file if it doesn't exist
	_, err = NewPrompt(promptFn)
	Ck(err)

	// If the test file is newer than any input files, then include
	// the test results in the prompt; otherwise, clear the test file
	testResults := ""
	testStat, err := os.Stat(testFn)
	if os.IsNotExist(err) {
		err = nil
	} else {
		Ck(err)
		// Get the list of input files
		p, err := getPrompt(promptFn)
		Ck(err)
		inFns := p.In
		// Check if the test file is newer than any input files
		for _, fn := range inFns {
			inStat, err := os.Stat(fn)
			Ck(err)
			if testStat.ModTime().After(inStat.ModTime()) {
				// Include the test results in the prompt
				buf, err := ioutil.ReadFile(testFn)
				Ck(err)
				testResults = string(buf)
				break
			}
		}
	}
	if len(testResults) == 0 {
		// Clear the test file
		Pl("Clearing test file")
		err = ioutil.WriteFile(testFn, []byte{}, 0644)
		Ck(err)
	}

	var p *Prompt
	p, err = getPrompt(promptFn)
	Ck(err)

	for i := 0; i < len(args); i++ {
		cmd := args[i]
		Pl("aidda: running subcommand", cmd)
		switch cmd {
		case "init":
			// Already done by this point, so this is a no-op
		case "commit":
			err = commit(g)
			Ck(err)
		case "generate":
			err = getChanges(g, p, testResults)
			Ck(err)
		case "test":
			err = runTest(testFn)
			Ck(err)
		case "auto":
			// Decide based on timestamps
			promptInfo, err := os.Stat(promptFn)
			Ck(err)
			if !promptInfo.ModTime().IsZero() {
				// Get the list of output files
				p, err := getPrompt(promptFn)
				Ck(err)
				commitNeeded := false
				for _, outFn := range p.Out {
					outInfo, err := os.Stat(outFn)
					if os.IsNotExist(err) {
						// If any output file does not exist, treat as need to generate
						commitNeeded = true
						break
					} else {
						Ck(err)
						if outInfo.ModTime().After(promptInfo.ModTime()) {
							commitNeeded = true
							break
						}
					}
				}
				if commitNeeded {
					args = append(args, "commit")
				} else {
					args = append(args, "generate")
				}
			} else {
				return fmt.Errorf("prompt file does not exist or has invalid modification time")
			}
		default:
			PrintUsageAndExit()
		}
	}

	return
}

func PrintUsageAndExit() {
	fmt.Println("Usage: go run main.go {subcommand ...}")
	fmt.Println("Subcommands:")
	fmt.Println("  commit    - Commit the current state")
	fmt.Println("  generate  - Generate changes from GPT based on the prompt")
	fmt.Println("  test      - Run tests and include the results in the prompt file")
	fmt.Println("  auto      - Automatically run generate or commit based on file timestamps")
	os.Exit(1)
}

// Prompt is a struct that represents a prompt
type Prompt struct {
	Sysmsg string
	In     []string
	Out    []string
	Txt    string
}

// NewPrompt opens or creates a prompt object
func NewPrompt(path string) (p *Prompt, err error) {
	defer Return(&err)
	// Check if the file exists
	_, err = os.Stat(path)
	if os.IsNotExist(err) {
		err = createPromptFile(path)
		Ck(err)
	} else {
		Ck(err)
	}
	p, err = readPrompt(path)
	Ck(err)
	return
}

// readPrompt reads a prompt file
func readPrompt(path string) (p *Prompt, err error) {
	defer Return(&err)
	p = &Prompt{}

	// Read entire content of the file
	rawBuf, err := ioutil.ReadFile(path)
	Ck(err)
	// Process directives
	// Lines that start with . are directives
	lines := []string{}
	rawLines := strings.Split(string(rawBuf), "\n")
	for i, line := range rawLines {
		// Ensure the first line doesn't start with a # (the default
		// prompt file starts with a comment; we want to make sure
		// the user edits it)
		if i == 0 && strings.HasPrefix(line, "#") {
			return nil, fmt.Errorf("prompt file must not start with a comment")
		}

		// Ensure there is a blank line after the first line
		if i == 1 {
			// trim leading and trailing whitespace
			trimmedLine := strings.TrimSpace(line)
			if trimmedLine != "" {
				// spew.Dump(line)
				return nil, fmt.Errorf("prompt file must have a blank line after the first line, just like a commit message")
			}
		}

		// .stop directive stops reading the prompt file
		if strings.HasPrefix(line, ".stop") {
			break
		}

		lines = append(lines, line)
	}
	// Remove empty lines at the end
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}

	// Find the index where headers start
	hdrStart := len(lines)
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			// empty line means headers start after this line
			break
		}
		if strings.Contains(line, ":") {
			// Found a header
			hdrStart = i
		} else {
			// continuation line
			continue
		}
	}

	if hdrStart >= len(lines) {
		return nil, fmt.Errorf("no headers found at the end of the prompt file")
	}

	// Extract headers
	headers := lines[hdrStart:]
	headerMap, err := extractHeaders(headers)
	if err != nil {
		return nil, err
	}

	// Use the prompt text excluding headers as the prompt and commit message
	p.Txt = strings.Join(lines[:hdrStart], "\n")
	Pl(p.Txt)

	// Process headers
	p.Sysmsg = strings.TrimSpace(headerMap["Sysmsg"])
	inStr := strings.TrimSpace(headerMap["In"])
	outStr := strings.TrimSpace(headerMap["Out"])

	// Filenames are space-separated
	p.In = strings.Fields(inStr)
	p.Out = strings.Fields(outStr)

	// Files are relative to the parent of the .aidda directory
	// unless they are absolute paths
	aiddaDir := filepath.Dir(path)
	parentDir := filepath.Dir(aiddaDir)

	// Convert p.In to absolute paths
	newIn := []string{}
	for _, f := range p.In {
		if f == "" {
			continue
		}
		if filepath.IsAbs(f) {
			newIn = append(newIn, f)
		} else {
			newIn = append(newIn, filepath.Join(parentDir, f))
		}
	}
	p.In = newIn

	// Similarly for p.Out
	newOut := []string{}
	for _, f := range p.Out {
		if f == "" {
			continue
		}
		if filepath.IsAbs(f) {
			newOut = append(newOut, f)
		} else {
			newOut = append(newOut, filepath.Join(parentDir, f))
		}
	}
	p.Out = newOut

	// If any input path is a directory, then replace it with the
	// list of files in that directory
	for i := 0; i < len(p.In); i++ {
		f := p.In[i]
		fi, err := os.Stat(f)
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %v", f, err)
		}
		if fi.IsDir() {
			files, err := getFilesInDir(f)
			Ck(err)
			p.In = append(p.In[:i], append(files, p.In[i+1:]...)...)
			i += len(files) - 1
		}
	}

	return p, nil
}

// extractHeaders extracts headers from a slice of lines and returns a map
func extractHeaders(headers []string) (map[string]string, error) {
	headerMap := make(map[string]string)
	var currentKey string
	for _, h := range headers {
		if h == "" {
			continue
		}
		if strings.HasPrefix(h, " ") || strings.HasPrefix(h, "\t") {
			// Continuation line
			if currentKey == "" {
				return nil, fmt.Errorf("continuation line found without a preceding header")
			}
			continuation := strings.TrimSpace(h)
			headerMap[currentKey] += " " + continuation
		} else {
			parts := strings.SplitN(h, ":", 2)
			if len(parts) != 2 {
				continue
			}
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			currentKey = key
			headerMap[key] = value
		}
	}
	return headerMap, nil
}

// getFilesInDir returns a list of files in a directory
func getFilesInDir(dir string) (files []string, err error) {
	defer Return(&err)

	// Get ignore list
	ig, err := gitignore.CompileIgnoreFile(ignoreFn)
	Ck(err)

	files = []string{}
	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		// If path is a directory, skip it
		if info.IsDir() {
			return nil
		}
		// Check if the file is in the ignore list
		if ig.MatchesPath(path) {
			return nil
		}
		// Only include regular files
		if !info.Mode().IsRegular() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	return files, err
}

// createPromptFile creates a new prompt file
func createPromptFile(path string) (err error) {
	defer Return(&err)
	file, err := os.Create(path)
	Ck(err)
	defer file.Close()

	// Get the list of files to process
	inFns, err := getFiles()
	Ck(err)
	outFns := make([]string, len(inFns))
	copy(outFns, inFns)

	// Filenames are space-separated
	inStr := strings.Join(inFns, " ")
	outStr := strings.Join(outFns, " ")

	// Write the initial prompt line and a blank line
	_, err = io.WriteString(file, "# write commit message here -- it will be used as the LLM prompt\n\n")
	Ck(err)

	// Write the headers at the end
	_, err = io.WriteString(file, fmt.Sprintf("Sysmsg: %s\n", DefaultSysmsg))
	Ck(err)
	_, err = io.WriteString(file, fmt.Sprintf("In: %s\n", inStr))
	Ck(err)
	_, err = io.WriteString(file, fmt.Sprintf("Out: %s\n", outStr))
	Ck(err)

	return
}

// ask asks the user a question and gets a response
func ask(question, deflt string, others ...string) (response string, err error) {
	defer Return(&err)
	var candidates []string
	candidates = append(candidates, strings.ToUpper(deflt))
	for _, o := range others {
		candidates = append(candidates, strings.ToLower(o))
	}
	for {
		fmt.Printf("%s [%s]: ", question, strings.Join(candidates, "/"))
		reader := bufio.NewReader(os.Stdin)
		response, err = reader.ReadString('\n')
		Ck(err)
		response = strings.TrimSpace(response)
		if response == "" {
			response = deflt
		}
		if len(others) == 0 {
			// If others is empty, return the response without
			// checking candidates
			return
		}
		// Check if the response is in the list of candidates
		for _, c := range candidates {
			if strings.ToLower(response) == strings.ToLower(c) {
				return
			}
		}
	}
}

func runTest(fn string) (err error) {
	defer Return(&err)
	Pf("Running tests\n")

	// Run go test -v
	stdout, stderr, _, _ := RunTee("go test -v")

	// Write test results to the file
	fh, err := os.Create(fn)
	Ck(err)
	_, err = fh.WriteString(Spf("\n\nstdout:\n%s\n\nstderr:%s\n\n", stdout, stderr))
	Ck(err)
	fh.Close()
	return err
}

func getChanges(g *core.Grokker, p *Prompt, testResults string) (err error) {
	defer Return(&err)

	if len(testResults) > 0 {
		Pl("Including test results in prompt")
	}
	prompt := Spf("%s\n\n%s", p.Txt, testResults)
	inFns := p.In
	outFns := p.Out
	var outFls []core.FileLang
	for _, fn := range outFns {
		lang, known, err := util.Ext2Lang(fn)
		Ck(err)
		if !known {
			Pf("Unknown language for file %s, defaulting to %s\n", fn, lang)
		}
		outFls = append(outFls, core.FileLang{File: fn, Language: lang})
	}

	sysmsg := p.Sysmsg
	if sysmsg == "" {
		Pf("Sysmsg header missing, using default.")
		sysmsg = DefaultSysmsg
	}
	Pf("Sysmsg: %s\n", sysmsg)

	msgs := []core.ChatMsg{
		core.ChatMsg{Role: "USER", Txt: prompt},
	}

	// Count tokens
	Pf("Token counts:\n")
	tcs := newTokenCounts(g)
	tcs.add("sysmsg", sysmsg)
	txt := ""
	for _, m := range msgs {
		txt += m.Txt
	}
	tcs.add("msgs", txt)
	for _, f := range inFns {
		var buf []byte
		buf, err = ioutil.ReadFile(f)
		Ck(err)
		txt = string(buf)
		tcs.add(f, txt)
	}
	tcs.showTokenCounts()

	Pl("Output files:")
	for _, f := range outFns {
		Pl(f)
	}

	Pf("Querying GPT...")
	// Start a goroutine to print dots while waiting for the response
	var stopDots = make(chan bool)
	go func() {
		for {
			select {
			case <-stopDots:
				return
			default:
				time.Sleep(1 * time.Second)
				fmt.Print(".")
			}
		}
	}()
	start := time.Now()
	resp, err := g.SendWithFiles(sysmsg, msgs, inFns, outFls)
	Ck(err)
	elapsed := time.Since(start)
	stopDots <- true
	close(stopDots)
	Pf(" got response in %s\n", elapsed)

	// ExtractFiles(outFls, promptFrag, dryrun, extractToStdout)
	err = core.ExtractFiles(outFls, resp, false, false)
	Ck(err)

	// Write entire response to .aidda/response
	Assert(len(baseDir) > 0, "baseDir not set")
	respFn := Spf("%s/.aidda/response", baseDir)
	err = ioutil.WriteFile(respFn, []byte(resp), 0644)
	Ck(err)

	// Write commit message to .aidda/commitmsg
	err = ioutil.WriteFile(commitMsgFn, []byte(p.Txt), 0644)
	Ck(err)

	return
}

type tokenCount struct {
	name  string
	text  string
	count int
}

type tokenCounts struct {
	g      *core.Grokker
	counts []tokenCount
}

// newTokenCounts creates a new tokenCounts object
func newTokenCounts(g *core.Grokker) *tokenCounts {
	return &tokenCounts{g: g}
}

// add adds a token count to a tokenCounts object
func (tcs *tokenCounts) add(name, text string) {
	count, err := tcs.g.TokenCount(text)
	Ck(err)
	tc := tokenCount{name: name, text: text, count: count}
	tcs.counts = append(tcs.counts, tc)
	return
}

// showTokenCounts shows the token counts for a slice of tokenCount
func (tcs *tokenCounts) showTokenCounts() {
	// First find max width of name
	maxNameLen := 0
	for _, tc := range tcs.counts {
		if len(tc.name) > maxNameLen {
			maxNameLen = len(tc.name)
		}
	}
	// Then print the counts
	total := 0
	format := fmt.Sprintf("    %%-%ds: %%7d\n", maxNameLen)
	for _, tc := range tcs.counts {
		Pf(format, tc.name, tc.count)
		total += tc.count
	}
	// Then print the total
	Pf(format, "total", total)
}

func getPrompt(promptFn string) (p *Prompt, err error) {
	defer Return(&err)

	// If AIDDA_EDITOR is set, open the editor where the users can
	// type a natural language instruction
	editor := envi.String("AIDDA_EDITOR", "")
	if editor != "" {
		Pf("Opening editor %s\n", editor)
		rc, err := RunInteractive(Spf("%s %s", editor, promptFn))
		Ck(err)
		Assert(rc == 0, "editor failed")
	}

	// Re-read the prompt file
	p, err = NewPrompt(promptFn)
	Ck(err)

	return p, err
}

func commit(g *core.Grokker) (err error) {
	defer Return(&err)
	var rc int

	// Check git status for uncommitted changes
	stdout, stderr, rc, err := Run("git status --porcelain", nil)
	Ck(err)
	if len(stdout) > 0 {
		Pl(string(stdout))
		Pl(string(stderr))
		// git add
		rc, err = RunInteractive("git add -A")
		Assert(rc == 0, "git add failed")
		Ck(err)
		// Read commit message from .aidda/commitmsg
		commitMsgBytes, err := ioutil.ReadFile(commitMsgFn)
		Ck(err)
		commitMsg := string(commitMsgBytes)
		// git commit
		stdout, stderr, rc, err = Run("git commit -F-", []byte(commitMsg))
		Pl(string(stdout))
		Pl(string(stderr))
		Assert(rc == 0, "git commit failed")
		Ck(err)
	} else {
		Pl("Nothing to commit")
	}

	return err
}

// getFiles returns a list of files to be processed
func getFiles() (files []string, err error) {
	defer Return(&err)

	// Get ignore list
	ignoreFn := ".aidda/ignore"
	ig, err := gitignore.CompileIgnoreFile(ignoreFn)
	Ck(err)

	// Get list of files recursively
	files = []string{}
	err = filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		// Ignore .git and .aidda directories
		if strings.Contains(path, ".git") || strings.Contains(path, ".aidda") {
			return nil
		}
		// Check if the file is in the ignore list
		if ig.MatchesPath(path) {
			return nil
		}
		// Skip non-files
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Add the file to the list
		files = append(files, path)
		return nil
	})
	Ck(err)
	return files, nil
}

// ensureIgnoreFile creates an ignore file if it doesn't exist
func ensureIgnoreFile(fn string) (err error) {
	defer Return(&err)
	// Check if the ignore file exists
	_, err = os.Stat(fn)
	if os.IsNotExist(err) {
		err = nil
		// Create the ignore file
		fh, err := os.Create(fn)
		Ck(err)
		defer fh.Close()
		// Write the default ignore patterns
		_, err = fh.WriteString(".git\n.idea\n.grok*\ngo.*\nnv.shada\n")
		Ck(err)
	}
	return err
}
