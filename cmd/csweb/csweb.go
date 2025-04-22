// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	stdregexp "regexp"
	"regexp/syntax"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/codesearch/index"
	"github.com/google/codesearch/regexp"
)

// TODO: experiment with full search history tracking and counting idea
// TODO: implement / search focus
// TODO: when viewing a file - show all matches in a sidebar or something
// TODO: add support for -f flag
// TODO: consider supporting -i, -l, -h, -b, -a, -c flags

//go:embed assets
var embeddedAssets embed.FS

var runtimeAssets = flag.String("assets", "", "todo")

var assets fs.FS
var templates Templates

func assert(truth bool, errs ...error) {
	if truth {
		return
	}
	buf := bytes.Buffer{}
	buf.WriteString("assertion failed")
	if len(errs) > 0 {
		buf.WriteRune(':')
	}
	buf.WriteRune('\n')
	for _, err := range errs {
		buf.WriteString(err.Error())
		buf.WriteRune('\n')
	}
	panic(buf.String())
}

type bufedHttpCtx struct {
	r   *http.Request
	buf bytes.Buffer
}

type bufedHttpErr struct {
	status int
	err    error
}

var notFoundBufedHttpErr *bufedHttpErr = &bufedHttpErr{
	status: http.StatusNotFound,
	err:    fmt.Errorf(http.StatusText(http.StatusNotFound)),
}

func (ctx *bufedHttpCtx) renderTemplate(name string, data any) *bufedHttpErr {
	if err := templates.Render(&ctx.buf, name, data); err != nil {
		return &bufedHttpErr{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("could not render %q template: %v", name, err),
		}
	}
	return nil
}

func bufedHttpHandlerFunc(h func(*bufedHttpCtx) *bufedHttpErr) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := bufedHttpCtx{r: r}
		if err := h(&ctx); err != nil {
			assert(err.status > 0)
			http.Error(w, err.err.Error(), err.status)
			return
		}
		ctx.buf.WriteTo(w)
	}
}

type query struct {
	re    *regexp.Regexp
	stdre *stdregexp.Regexp
}

func compileQuery(qarg string) (*query, error) {
	pat := "(?m)" + qarg
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, err
	}
	// NOTE: regex err was checked above ^
	stdre := stdregexp.MustCompile(re.Syntax.String())
	return &query{re: re, stdre: stdre}, nil
}

func getQueryExpr(expr string) string {
	return strings.TrimPrefix(expr, "(?m)")
}

func newBadQueryBufedHttpErr(err error) *bufedHttpErr {
	var syntaxErr *syntax.Error
	if errors.As(err, &syntaxErr) {
		syntaxErr.Expr = getQueryExpr(syntaxErr.Expr)
		err = syntaxErr
	}
	return &bufedHttpErr{
		status: http.StatusBadRequest,
		err:    fmt.Errorf("bad query: %v", err),
	}
}

func findRoot(ix *index.Index, name string) (string, bool) {
	root := ""
	for it := range ix.Roots().All() {
		it := it.String()
		if strings.HasPrefix(name, it) {
			root = it
		}
	}
	return root, root != ""
}

type breadcrumb struct {
	Basename string // see path.Base
	Dirname  string // see path.Dir
	IsDir    bool   // indicates whether Basename is a dir
	// IsOutOfRoot indicates whether this breadcrumb would illegally step
	// outside of root to do path traversal attack
	IsOutOfRoot bool
}

func collectBreadcrumbs(root, name string) ([]breadcrumb, error) {
	assert(name[len(name)-1] != '/', fmt.Errorf("target must not end with /"))

	breadcrumbs := make([]breadcrumb, 0)

	prevPartEnd := 0
	nel := len(name)
	for i, it := range name {
		atSeparator := it == '/'
		wouldEnd := i == nel-1
		if (!atSeparator && !wouldEnd) || i == 0 {
			continue
		}

		partEnd := i
		isDir := true
		if wouldEnd {
			partEnd = nel
			fi, err := os.Stat(name[:partEnd])
			if err != nil {
				return nil, fmt.Errorf("could not stat %q: %w", name, err)
			}
			isDir = fi.IsDir()
		}

		breadcrumbs = append(breadcrumbs, breadcrumb{
			Basename:    name[prevPartEnd+1 : partEnd],
			Dirname:     name[:prevPartEnd],
			IsDir:       isDir,
			IsOutOfRoot: strings.HasPrefix(root, name[:partEnd]) && root != name[:partEnd],
		})

		prevPartEnd = partEnd
	}

	return breadcrumbs, nil
}

func computeLinePad(maxLineNo int) int {
	return len(fmt.Sprintf("%d", maxLineNo)) + 1
}

type sourceLine struct {
	Line                  []byte
	LineNo                int
	PreciseMatchLocations [][]int
}

type sourceHunk struct {
	Lines []sourceLine
	// NOTE: make sure to init this with -1
	FirstNonContextLineNo int
}

// returns 0, false if contains no lines.
func (h *sourceHunk) getLastLineNoAssumeSorted() (int, bool) {
	if len(h.Lines) == 0 {
		return 0, false
	}
	return h.Lines[len(h.Lines)-1].LineNo, true
}

func (h *sourceHunk) maybeAppendLineCopy(line []byte, lineNo int, query *query) {
	if len(line) == 0 {
		return
	}

	lastLineNo, ok := h.getLastLineNoAssumeSorted()
	if ok && lastLineNo >= lineNo {
		return
	}

	sr := sourceLine{
		Line:                  make([]byte, len(line)),
		LineNo:                lineNo,
		PreciseMatchLocations: query.stdre.FindAllIndex(line, -1),
	}
	copy(sr.Line, line)

	if h.FirstNonContextLineNo == -1 && len(sr.PreciseMatchLocations) > 0 {
		h.FirstNonContextLineNo = lineNo
	}

	h.Lines = append(h.Lines, sr)
}

type fileSearchResult struct {
	Breadcrumbs []breadcrumb
	Hunks       []sourceHunk
}

func (f *fileSearchResult) shouldStartNewHunk(grepMatch *regexp.GrepMatch) bool {
	if len(f.Hunks) == 0 {
		return true
	}

	lastHunk := &f.Hunks[len(f.Hunks)-1]
	lastLineNo, ok := lastHunk.getLastLineNoAssumeSorted()
	if !ok {
		return false
	}

	lineNo := grepMatch.LineNo - len(grepMatch.PreContext)
	gap := lineNo - lastLineNo
	return gap > 0
}

func (f *fileSearchResult) appendGrepMatch(grepMatch *regexp.GrepMatch, query *query) {
	if f.shouldStartNewHunk(grepMatch) {
		initCap := len(grepMatch.PreContext) + 1 + len(grepMatch.PostContext)
		f.Hunks = append(f.Hunks, sourceHunk{
			Lines:                 make([]sourceLine, 0, initCap),
			FirstNonContextLineNo: -1,
		})
	}

	hunk := &f.Hunks[len(f.Hunks)-1]
	lineNo := grepMatch.LineNo - len(grepMatch.PreContext)

	// TODO: can stuff below be chained somehow?

	for _, line := range grepMatch.PreContext {
		hunk.maybeAppendLineCopy(line, lineNo, query)
		lineNo += 1
	}

	hunk.maybeAppendLineCopy(grepMatch.Line, lineNo, query)
	lineNo += 1

	for _, line := range grepMatch.PostContext {
		hunk.maybeAppendLineCopy(line, lineNo, query)
		lineNo += 1
	}
}

func handleIndex(ctx *bufedHttpCtx) *bufedHttpErr {
	qarg := ctx.r.FormValue("q")
	if qarg == "" {
		return ctx.renderTemplate("page.index", nil)
	}

	start := time.Now()

	query, err := compileQuery(qarg)
	if err != nil {
		return newBadQueryBufedHttpErr(err)
	}
	g := regexp.Grep{
		Regexp:      query.re,
		N:           true,
		Limit:       10000,
		PreContext:  1,
		PostContext: 1,
	}
	ix := index.Open(index.File())
	q := index.RegexpQuery(query.re.Syntax)
	post := ix.PostingQuery(q)

	maxLineNo := 0
	searchResults := make([]fileSearchResult, 0)

	for _, fileid := range post {
		if g.Limited {
			break
		}

		searchResult := fileSearchResult{}

		filename := ix.Name(fileid).String()
		file, err := os.Open(filename)
		if err != nil {
			if i := strings.Index(filename, ".zip\x01"); i >= 0 {
				assert(false, fmt.Errorf("TODO: handle zips"))
			}
			continue
		}

		for grepMatch := range g.ReaderSeq(file) {
			searchResult.appendGrepMatch(grepMatch, query)
		}

		file.Close()

		// TODO: collect grep errs and render them?
		err = g.Err()
		if err != nil {
			assert(false, fmt.Errorf("TODO: accumulate and report grep errs?"))
		}

		if len(searchResult.Hunks) > 0 {
			root, foundRoot := findRoot(ix, filename)
			assert(foundRoot)

			searchResult.Breadcrumbs, err = collectBreadcrumbs(root, filename)
			if err != nil {
				return &bufedHttpErr{
					status: http.StatusInternalServerError,
					err:    fmt.Errorf("could not collect breadcrumbs: %w", err),
				}
			}

			lastHunk := &searchResult.Hunks[len(searchResult.Hunks)-1]
			lastHunkLastLineNo, ok := lastHunk.getLastLineNoAssumeSorted()
			assert(ok)

			maxLineNo = max(maxLineNo, lastHunkLastLineNo)
			searchResults = append(searchResults, searchResult)
		}
	}

	return ctx.renderTemplate("page.index", map[string]any{
		"Query":         qarg,
		"SearchResults": searchResults,
		"LinePad":       computeLinePad(maxLineNo),
		"MatchCount":    g.Matches,
		"TimeElapsed":   time.Since(start).Seconds(),
	})
}

type dirEntry struct {
	Name  string
	IsDir bool
}

type sourceDir struct {
	Breadcrumbs []breadcrumb
	Entries     []dirEntry
}

func readDir(root, name string, query *query) (*sourceDir, error) {
	dirEntries, err := os.ReadDir(name)
	if err != nil {
		return nil, fmt.Errorf("could not read dir: %w", err)
	}

	breadcrumbs, err := collectBreadcrumbs(root, name)
	if err != nil {
		return nil, fmt.Errorf("could not collect breadcrumbs: %w", err)
	}

	ret := sourceDir{
		Breadcrumbs: breadcrumbs,
		Entries:     make([]dirEntry, len(dirEntries)),
	}

	// TODO: match and highlight dits with matches?
	for i, it := range dirEntries {
		ret.Entries[i] = dirEntry{
			Name:  it.Name(),
			IsDir: it.IsDir(),
		}
	}

	return &ret, nil
}

// isText reports whether a significant prefix of s looks like correct UTF-8;
// that is, if it is likely that s is human-readable text.
func isText(s []byte) bool {
	const max = 1024 // at least utf8.UTFMax
	if len(s) > max {
		s = s[0:max]
	}
	for i, c := range string(s) {
		if i+utf8.UTFMax > len(s) {
			// last char may be incomplete - ignore
			break
		}
		if c == 0xFFFD || c < ' ' && c != '\n' && c != '\t' && c != '\f' {
			// decoding error or control character - not a text file
			return false
		}
	}
	return true
}

type sourceFile struct {
	Breadcrumbs []breadcrumb
	LinePad     int
	Lines       []sourceLine
	MatchCount  int
}

var nl = []byte("\n")

func readAndMatchFile(root, name string, query *query) (*sourceFile, error) {
	// TODO: file size limit ?
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("could not read %q: %w", name, err)
	}

	if !isText(data) {
		// TODO: zips?
		// TODO: report error
		assert(false, fmt.Errorf("requested non text file"))
	}

	breadcrumbs, err := collectBreadcrumbs(root, name)
	if err != nil {
		return nil, fmt.Errorf("could not collect breadcrumbs: %w", err)
	}

	ret := sourceFile{
		Breadcrumbs: breadcrumbs,
		LinePad:     computeLinePad(bytes.Count(data, nl) + 1),
		Lines:       make([]sourceLine, 0),
		MatchCount:  0,
	}

	for lineNo := 1; len(data) > 0; lineNo += 1 {
		sourceLine := sourceLine{LineNo: lineNo}
		sourceLine.Line, data, _ = bytes.Cut(data, nl)

		if query != nil {
			if query.re.Match(sourceLine.Line, lineNo == 1, true) != -1 {
				sourceLine.PreciseMatchLocations = query.stdre.FindAllIndex(sourceLine.Line, -1)
				ret.MatchCount += 1
			}
		}

		ret.Lines = append(ret.Lines, sourceLine)

	}

	return &ret, nil
}

func handleFilepath(ctx *bufedHttpCtx) *bufedHttpErr {
	name := strings.TrimSuffix(ctx.r.URL.Path, "/")
	if strings.HasPrefix(name, "/") && filepath.IsAbs(name[1:]) {
		// Turn /c:/foo into c:/foo on Windows.
		name = name[1:]
	}

	start := time.Now()

	ix := index.Open(index.File())

	// NOTE: this prevents serving filenames that aren't at known roots
	root, foundRoot := findRoot(ix, name)
	if !foundRoot {
		return notFoundBufedHttpErr
	}

	// TODO zips?
	info, err := os.Stat(name)
	if err != nil {
		if os.IsNotExist(err) {
			return notFoundBufedHttpErr
		}
		return &bufedHttpErr{
			status: http.StatusInternalServerError,
			err:    err,
		}
	}

	qarg := ctx.r.FormValue("q")
	var query *query
	if qarg != "" {
		query, err = compileQuery(qarg)
		if err != nil {
			return newBadQueryBufedHttpErr(err)
		}
	}

	if info.IsDir() {
		sourceDir, err := readDir(root, name, query)
		if err != nil {
			return &bufedHttpErr{
				status: http.StatusInternalServerError,
				err:    fmt.Errorf("could not read dir: %w", err),
			}
		}
		return ctx.renderTemplate("page.filepath", map[string]any{
			"Query":       qarg,
			"SourceDir":   sourceDir,
			"TimeElapsed": time.Since(start).Seconds(),
		})
	}

	sourceFile, err := readAndMatchFile(root, name, query)
	if err != nil {
		return &bufedHttpErr{
			status: http.StatusInternalServerError,
			err:    err,
		}
	}
	return ctx.renderTemplate("page.filepath", map[string]any{
		"Query":       qarg,
		"SourceFile":  sourceFile,
		"TimeElapsed": time.Since(start).Seconds(),
	})
}

func erringMain() error {
	flag.Parse()

	var err error
	if runtimeAssets != nil && *runtimeAssets != "" {
		assets = os.DirFS(*runtimeAssets)

		templates, err = InitDynamicTemplates(assets, "*.gohtml")
		if err != nil {
			return fmt.Errorf("could not init dynamic templates: %w", err)
		}
	} else {
		assets, err = fs.Sub(embeddedAssets, "assets")
		if err != nil {
			return fmt.Errorf("invalid assets subdir: %w", err)
		}

		templates, err = InitStaticTemplates(assets, "*.gohtml")
		if err != nil {
			return fmt.Errorf("could not init static templates: %w", err)
		}
	}

	http.Handle("GET /assets/{asset...}", http.StripPrefix("/assets", http.FileServerFS(assets)))
	http.HandleFunc("GET /{$}", bufedHttpHandlerFunc(handleIndex))
	http.HandleFunc("GET /{filepath...}", bufedHttpHandlerFunc(handleFilepath))

	return http.ListenAndServe("localhost:2473", nil)
}

func main() {
	if err := erringMain(); err != nil {
		fmt.Fprintf(os.Stderr, "fucky wucky! %s\n", err)
		os.Exit(42)
	}
}
