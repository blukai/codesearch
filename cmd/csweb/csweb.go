// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	stdregexp "regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/codesearch/index"
	"github.com/google/codesearch/regexp"
)

// TODO: generalize + reuse precise match template?
// TODO: unfuck preciseGrepMatch, it is not as genral as i thought it would be
// TODO: render one line of context around matches on search page
// TODO: merge ~overlaping contexts in some way
// TODO: experiment with full search history tracking and counting idea
// TODO: render timings in footer
// TODO: implement / search focus
// TODO: when viewing a file - show all matches in a sidebar or something

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

func newBadQueryBufedHttpErr(regexpCompileErr error) *bufedHttpErr {
	return &bufedHttpErr{
		status: http.StatusBadRequest,
		err:    fmt.Errorf("bad query: %v", regexpCompileErr),
	}
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

// TODO: organize return into a tuple-like struct for convenience, to avoid a
// need to carry two structs around both of which can be nil!
func compileQuery(qarg string) (*regexp.Regexp, *stdregexp.Regexp, error) {
	pat := "(?m)" + qarg
	re, err := regexp.Compile(pat)
	if err != nil {
		return nil, nil, err
	}
	// NOTE: regex err was checked above ^
	stdre := stdregexp.MustCompile(re.Syntax.String())
	return re, stdre, nil
}

// func getQueryString(re *regexp.Regexp) string {
// 	return strings.TrimPrefix(re.String(), "(?m)")
// }

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
	linePad := len(fmt.Sprintf("%d", maxLineNo))
	linePad = (linePad+2+7)&^7 - 2
	return linePad
}

// TODO: get rid of this. only usable in search results. factor this into
// something like hunk + add support for pre and post context lines.
type preciseGrepMatch struct {
	Locations [][]int
	regexp.GrepMatch
}

func newPreciseGrepMatch(
	src *regexp.GrepMatch,
	stdre *stdregexp.Regexp,
) preciseGrepMatch {
	dst := preciseGrepMatch{}

	dst.Locations = stdre.FindAllIndex(src.Line, -1)
	assert(len(dst.Locations) > 0)

	// NOTE: this function get's called from the loop, deep copy is the
	// only way to get "current" line into dst.
	dst.Line = make([]byte, len(src.Line))
	copy(dst.Line, src.Line)

	dst.LineNo = src.LineNo

	return dst
}

func handleIndex(ctx *bufedHttpCtx) *bufedHttpErr {
	qarg := ctx.r.FormValue("q")
	if qarg == "" {
		return ctx.renderTemplate("page.index", nil)
	}

	start := time.Now()

	re, stdre, err := compileQuery(qarg)
	if err != nil {
		return newBadQueryBufedHttpErr(err)
	}
	g := regexp.Grep{
		Regexp: re,
		Limit:  10000,
		N:      true,
	}
	var fre *regexp.Regexp
	if farg := ctx.r.FormValue("f"); farg != "" {
		fre, err = regexp.Compile(farg)
		if err != nil {
			return &bufedHttpErr{
				status: http.StatusBadRequest,
				err:    fmt.Errorf("bad -f flag: %v", err),
			}
		}
	}

	ix := index.Open(index.File())
	q := index.RegexpQuery(re.Syntax)
	post := ix.PostingQuery(q)

	if fre != nil {
		filtered := make([]int, 0, len(post))
		for _, fileid := range post {
			name := ix.Name(fileid)
			if fre.MatchString(name.String(), true, true) == -1 {
				continue
			}
			filtered = append(filtered, fileid)
		}
		post = filtered
	}

	type matchedFile struct {
		Breadcrumbs []breadcrumb
		Matches     []preciseGrepMatch
	}

	matchedFiles := make([]matchedFile, 0)
	maxLineNo := 0

	// 	var zipFile   string
	// 	var zipReader *zip.ReadCloser
	// 	var zipMap    map[string]*zip.File

	for _, fileid := range post {
		if g.Limited {
			break
		}

		filename := ix.Name(fileid).String()
		matches := make([]preciseGrepMatch, 0)

		file, err := os.Open(filename)
		if err != nil {
			if i := strings.Index(filename, ".zip\x01"); i >= 0 {
				assert(false, fmt.Errorf("TODO: handle zips"))
				// zfile, zname := name[:i+4], name[i+5:]
				// if zfile != zipFile {
				// 	if zipReader != nil {
				// 		zipReader.Close()
				// 		zipMap = nil
				// 	}
				// 	zipFile = zfile
				// 	zipReader, err = zip.OpenReader(zfile)
				// 	if err != nil {
				// 		zipReader = nil
				// 	}
				// 	if zipReader != nil {
				// 		zipMap = make(map[string]*zip.File)
				// 		for _, file := range zipReader.File {
				// 			zipMap[file.Name] = file
				// 		}
				// 	}
				// }
				// file := zipMap[zname]
				// if file != nil {
				// 	r, err := file.Open()
				// 	if err != nil {
				// 		continue
				// 	}
				// 	renderFileMatches(w, r, name, &g, stdre)
				// 	r.Close()
				// 	continue
				// }
			}

			continue
		}

		for match := range g.ReaderSeq(file) {
			maxLineNo = max(maxLineNo, match.LineNo)
			matches = append(matches, newPreciseGrepMatch(&match, stdre))
		}

		file.Close()

		// TODO: collect grep errs and render them?
		if err := g.Err(); err != nil {
			assert(false, fmt.Errorf("unimplemented"))
		}

		if len(matches) > 0 {
			// TODO: might want to cache this
			root, foundRoot := findRoot(ix, filename)
			assert(foundRoot)

			breadcrumbs, err := collectBreadcrumbs(root, filename)
			if err != nil {
				return &bufedHttpErr{
					status: http.StatusInternalServerError,
					err:    fmt.Errorf("could not collect breadcrumbs: %w", err),
				}
			}

			matchedFiles = append(matchedFiles, matchedFile{
				Breadcrumbs: breadcrumbs,
				Matches:     matches,
			})
		}
	}

	return ctx.renderTemplate("page.index", map[string]any{
		"Query":       qarg,
		"Files":       matchedFiles,
		"LinePad":     computeLinePad(maxLineNo),
		"MatchCount":  g.Matches,
		"TimeElapsed": time.Since(start).Seconds(),
	})
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

type dirEntry struct {
	Name string
}

func collectDirEntries(path string) ([]dirEntry, error) {
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("could not read dir: %w", err)
	}
	ret := make([]dirEntry, len(dirEntries))
	// TODO: match and highlight dits with matches?
	for i, it := range dirEntries {
		ret[i] = dirEntry{Name: it.Name()}
	}
	return ret, nil
}

type sourceLine struct {
	Line                  []byte
	LineNo                int
	PreciseMatchLocations [][]int
}

type sourceFile struct {
	LinePad int
	Lines   []sourceLine
}

var nl = []byte("\n")

func readSourceFile(filename string, re *regexp.Regexp, stdre *stdregexp.Regexp) (*sourceFile, error) {
	// TODO: protect from huge files
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("could not read %q: %w", filename, err)
	}

	ret := sourceFile{
		LinePad: computeLinePad(bytes.Count(data, nl) + 1),
		Lines:   make([]sourceLine, 0),
	}

	for lineNo := 1; len(data) > 0; lineNo += 1 {
		sourceLine := sourceLine{LineNo: lineNo}
		sourceLine.Line, data, _ = bytes.Cut(data, nl)

		if re != nil {
			assert(stdre != nil)
			if re.Match(sourceLine.Line, lineNo == 1, true) != -1 {
				sourceLine.PreciseMatchLocations = stdre.FindAllIndex(sourceLine.Line, -1)
			}
		}

		ret.Lines = append(ret.Lines, sourceLine)

	}

	return &ret, nil
}

func handleFilepath(ctx *bufedHttpCtx) *bufedHttpErr {
	filename := strings.TrimSuffix(ctx.r.URL.Path, "/")
	if strings.HasPrefix(filename, "/") && filepath.IsAbs(filename[1:]) {
		// Turn /c:/foo into c:/foo on Windows.
		filename = filename[1:]
	}

	ix := index.Open(index.File())

	// NOTE: this prevents serving filenames that aren't at known roots
	root, foundRoot := findRoot(ix, filename)
	if !foundRoot {
		return notFoundBufedHttpErr
	}

	// TODO zips
	info, err := os.Stat(filename)
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
	breadcrumbs, err := collectBreadcrumbs(root, filename)
	if err != nil {
		return &bufedHttpErr{
			status: http.StatusInternalServerError,
			err:    fmt.Errorf("could not collect breadcrumbs: %w", err),
		}
	}

	if info.IsDir() {
		dirEntries, err := collectDirEntries(filename)
		if err != nil {
			return &bufedHttpErr{
				status: http.StatusInternalServerError,
				err:    err,
			}
		}
		return ctx.renderTemplate("page.filepath", map[string]any{
			"Query":       qarg,
			"Breadcrumbs": breadcrumbs,
			"DirEntries":  dirEntries,
		})
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		return &bufedHttpErr{
			status: http.StatusInternalServerError,
			err:    err,
		}
	}

	if !isText(data) {
		assert(false, fmt.Errorf("requested non text file. implmenet me serving it"))
	}

	var re *regexp.Regexp
	var stdre *stdregexp.Regexp
	if qarg != "" {
		re, stdre, err = compileQuery(qarg)
		if err != nil {
			return newBadQueryBufedHttpErr(err)
		}
	}

	sourceFile, err := readSourceFile(filename, re, stdre)
	if err != nil {
		return &bufedHttpErr{
			status: http.StatusInternalServerError,
			err:    err,
		}
	}

	return ctx.renderTemplate("page.filepath", map[string]any{
		"Query":       qarg,
		"Breadcrumbs": breadcrumbs,
		"SourceFile":  sourceFile,
		// "MatchCount":  g.Matches,
		// "TimeElapsed": time.Since(start).Seconds(),
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
