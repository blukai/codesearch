// Copyright 2020 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	stdregexp "regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/codesearch/index"
	"github.com/google/codesearch/regexp"
)

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

func renderFileMatches(w http.ResponseWriter, r io.Reader, name string, g *regexp.Grep, stdre *stdregexp.Regexp) (didErr bool) {
	matchData := map[string]any{
		// TODO: do i need to escape or replace # with something else for zip files?
		"Filename": name,
		// TODO: unhardcode "(?m)"
		"Query": strings.TrimPrefix(g.Regexp.String(), "(?m)"),
	}

	for match := range g.ReaderSeq(r) {
		matchData["LineNo"] = match.LineNo
		matchData["Line"] = string(match.Line)
		matchData["LineMatches"] = stdre.FindAllIndex(match.Line, -1)

		if err := templates.Render(w, "page.index.match", matchData); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			// template is broken
			didErr = true
			return
		}

		if err := g.Err(); err != nil {
			// TODO: make this red or something?
			// TODO: don't just print, exec template
			fmt.Fprintf(g.Stderr, "%s: %v\n", name, err)
			didErr = true
		}
	}

	return
}

func handleIndex(w http.ResponseWriter, r *http.Request) {
	qarg := r.FormValue("q")
	headData := map[string]any{"Query": qarg}
	if err := templates.Render(w, "page.index.head", headData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if qarg == "" {
		if err := templates.Render(w, "page.index.foot", nil); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	re, stdre, err := compileQuery(qarg)
	if err != nil {
		// TODO: template this
		fmt.Fprintf(w, "Bad query: %v\n", err)
		return
	}
	g := regexp.Grep{
		Regexp: re,
		Limit:  10000,
		N:      true,
	}

	var fre *regexp.Regexp
	if farg := r.FormValue("f"); farg != "" {
		fre, err = regexp.Compile(farg)
		if err != nil {
			// TODO: template this
			fmt.Fprintf(w, "Bad -f flag: %v\n", err)
			return
		}
	}

	start := time.Now()

	ix := index.Open(index.File())
	q := index.RegexpQuery(re.Syntax)
	post := ix.PostingQuery(q)

	if fre != nil {
		fnames := make([]int, 0, len(post))
		for _, fileid := range post {
			name := ix.Name(fileid)
			if fre.MatchString(name.String(), true, true) < 0 {
				continue
			}
			fnames = append(fnames, fileid)
		}
		post = fnames
	}

	var (
		zipFile   string
		zipReader *zip.ReadCloser
		zipMap    map[string]*zip.File
	)

	for _, fileid := range post {
		if g.Limited {
			break
		}
		name := ix.Name(fileid).String()
		file, err := os.Open(name)
		if err != nil {
			if i := strings.Index(name, ".zip\x01"); i >= 0 {
				zfile, zname := name[:i+4], name[i+5:]
				if zfile != zipFile {
					if zipReader != nil {
						zipReader.Close()
						zipMap = nil
					}
					zipFile = zfile
					zipReader, err = zip.OpenReader(zfile)
					if err != nil {
						zipReader = nil
					}
					if zipReader != nil {
						zipMap = make(map[string]*zip.File)
						for _, file := range zipReader.File {
							zipMap[file.Name] = file
						}
					}
				}
				file := zipMap[zname]
				if file != nil {
					r, err := file.Open()
					if err != nil {
						continue
					}
					renderFileMatches(w, r, name, &g, stdre)
					r.Close()
					continue
				}
			}
			continue
		}
		renderFileMatches(w, file, name, &g, stdre)
		file.Close()
	}

	footData := map[string]any{"MatchCount": g.Matches, "TimeElapsed": time.Since(start).Seconds()}
	if err := templates.Render(w, "page.index.foot", footData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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

func renderBreadcrumbs(w http.ResponseWriter, filename, root, qarg string) (didErr bool) {
	type breadcrumb struct {
		Part         string
		Backtracking bool
	}
	breadcrumbs := make([]breadcrumb, 0)
	breadcrumbOffset := 0
	for i, part := range filename {
		if part == '/' {
			if i > 0 {
				breadcrumbs = append(breadcrumbs, breadcrumb{
					Part:         filename[breadcrumbOffset:i],
					Backtracking: strings.HasPrefix(root, filename[:i]) && root != filename[:i],
				})
			}
			breadcrumbOffset = i + 1
		}
	}
	assert(filename[len(filename)-1] != '/')
	breadcrumbs = append(breadcrumbs, breadcrumb{Part: filename[breadcrumbOffset:]})

	breadcrumbsData := map[string]any{"Query": qarg, "Breadcrumbs": breadcrumbs}
	if err := templates.Render(w, "page.filename.breadcrumbs", breadcrumbsData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	return false
}

func renderDir(w http.ResponseWriter, filename, qarg string) (didErr bool) {
	dirEntries, err := os.ReadDir(filename)
	if err != nil {
		// TODO: template this
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}
	// TODO: match and highlight dits with matches?
	dirs := slices.Collect(func(yield func(string) bool) {
		for _, de := range dirEntries {
			if !yield(filename + "/" + de.Name()) {
				return
			}
		}
	})
	dirsData := map[string]any{"Query": qarg, "Dirs": dirs}
	if err := templates.Render(w, "page.filename.dirs", dirsData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return true
	}

	return false
}

var nl = []byte("\n")

func renderLines(w http.ResponseWriter, qarg string, data []byte) (didErr bool) {
	err := templates.Render(w, "page.filename.lines.pre", nil)
	assert(err == nil, err)
	defer func(w http.ResponseWriter) {
		err := templates.Render(w, "page.filename.lines.post", nil)
		assert(err == nil, err)
	}(w)

	var re *regexp.Regexp
	var stdre *stdregexp.Regexp
	if qarg != "" {
		re, stdre, _ = compileQuery(qarg)
	}

	lineCount := bytes.Count(data, nl) + 1
	linePad := len(fmt.Sprintf("%d", lineCount))
	linePad = (linePad+2+7)&^7 - 2

	lineData := map[string]any{
		"LinePad": linePad,
		"LineNo":  1,
	}

	for lineNo := 1; len(data) > 0; lineNo += 1 {
		var line []byte
		line, data, _ = bytes.Cut(data, nl)

		lineData["LineNo"] = lineNo
		lineData["Line"] = string(line)
		lineData["LineMatches"] = nil

		if re != nil {
			if re.Match(line, lineNo == 1, true) != -1 {
				lineData["LineMatches"] = stdre.FindAllIndex(line, -1)
			}
		}

		if err := templates.Render(w, "page.filename.lines.line", lineData); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return true
		}
	}

	return false
}

func handleFile(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	filename := r.URL.Path
	if strings.HasPrefix(filename, "/") && filepath.IsAbs(filename[1:]) {
		// Turn /c:/foo into c:/foo on Windows.
		filename = filename[1:]
	}

	ix := index.Open(index.File())

	// NOTE: this prevents serving filenames that aren't at known roots
	root := ""
	for it := range ix.Roots().All() {
		it := it.String()
		if strings.HasPrefix(filename, it) {
			root = it
		}
	}
	if root == "" {
		http.NotFound(w, r)
		return
	}

	// TODO maybe trim file by ix.roots
	// TODO zips
	info, err := os.Stat(filename)
	if err != nil {
		if os.IsNotExist(err) {
			http.NotFound(w, r)
			return
		}
		// TODO: template this
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	qarg := r.FormValue("q")
	headData := map[string]any{"Query": qarg}
	if err := templates.Render(w, "page.filename.head", headData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if renderBreadcrumbs(w, filename, root, qarg) {
		return
	}

	if info.IsDir() {
		if renderDir(w, filename, qarg) {
			return
		}
		footData := map[string]any{"TimeElapsed": time.Since(start).Seconds()}
		if err := templates.Render(w, "page.filename.foot", footData); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	data, err := os.ReadFile(filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if !isText(data) {
		http.Error(w, "requested non text file. implmenet me serving it", http.StatusBadRequest)
		return
	}

	if renderLines(w, qarg, data) {
		return
	}
	footData := map[string]any{"TimeElapsed": time.Since(start).Seconds()}
	if err := templates.Render(w, "page.filename.foot", footData); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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
	http.HandleFunc("GET /{$}", handleIndex)
	http.HandleFunc("GET /{file...}", handleFile)

	return http.ListenAndServe("localhost:2473", nil)
}

func main() {
	if err := erringMain(); err != nil {
		fmt.Fprintf(os.Stderr, "fucky wucky! %s\n", err)
		os.Exit(42)
	}
}
