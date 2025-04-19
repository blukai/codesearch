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
	"html"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	stdregexp "regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/codesearch/index"
	"github.com/google/codesearch/regexp"
)

var (
	verboseFlag = flag.Bool("verbose", false, "print extra information")
)

func main() {
	http.HandleFunc("/", home)
	http.Handle("/_static/", http.FileServer(http.FS(static)))
	http.HandleFunc("/show/", show)
	log.Fatal(http.ListenAndServe("localhost:2473", nil))
}

//go:embed _static
var static embed.FS

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

func markLineMatches(line []byte, stdre *stdregexp.Regexp) string {
	e := html.EscapeString
	b := strings.Builder{}
	lastLocEnd := 0
	for _, loc := range stdre.FindAllIndex(line, -1) {
		prefix := line[lastLocEnd:loc[0]]
		stem := line[loc[0]:loc[1]]
		lastLocEnd = loc[1]

		b.WriteString(e(string(prefix)))
		b.WriteString(`<mark><b>`)
		b.WriteString(e(string(stem)))
		b.WriteString(`</b></mark>`)
	}
	b.WriteString(e(string(line[lastLocEnd:])))
	return b.String()
}

func serveMatches(w io.Writer, r io.Reader, name string, g *regexp.Grep, stdre *stdregexp.Regexp) {
	e := html.EscapeString
	for match := range g.ReaderSeq(r) {
		nl := ""
		if len(match.Line) == 0 || match.Line[len(match.Line)-1] != '\n' {
			nl = "\n"
		}

		fmt.Fprintf(w, "<a href=\"/show/%s?q=%s&l=%d\">%s:%d</a>:%s%s",
			e(strings.ReplaceAll(name, "#", ">")),
			e(strings.TrimPrefix(g.Regexp.String(), "(?m)")),
			match.LineNo,
			e(name),
			match.LineNo,
			markLineMatches(match.Line, stdre),
			nl,
		)

		if err := g.Err(); err != nil {
			// TODO: make this red or something?
			fmt.Fprintf(g.Stderr, "%s: %v\n", e(name), err)
		}
	}
}

func home(w http.ResponseWriter, r *http.Request) {
	qarg := r.FormValue("q")
	w.Write([]byte(strings.ReplaceAll(homePage, "QUERY", html.EscapeString(qarg))))
	if qarg == "" {
		return
	}

	re, stdre, err := compileQuery(qarg)
	if err != nil {
		fmt.Fprintf(w, "Bad query: %v\n", err)
		return
	}
	g := regexp.Grep{
		Regexp: re,
		Limit:  10000,
		N:      true,
	}

	var fre *regexp.Regexp
	farg := r.FormValue("f")
	if farg != "" {
		fre, err = regexp.Compile(farg)
		if err != nil {
			fmt.Fprintf(w, "Bad -f flag: %v\n", err)
			return
		}
	}

	q := index.RegexpQuery(re.Syntax)
	if *verboseFlag {
		log.Printf("query: %s\n", q)
	}

	start := time.Now()

	ix := index.Open(index.File())
	ix.Verbose = *verboseFlag
	post := ix.PostingQuery(q)
	if *verboseFlag {
		fmt.Fprintf(w, "post query identified %d possible files\n", len(post))
	}

	if fre != nil {
		fnames := make([]int, 0, len(post))

		for _, fileid := range post {
			name := ix.Name(fileid)
			if fre.MatchString(name.String(), true, true) < 0 {
				continue
			}
			fnames = append(fnames, fileid)
		}

		if *verboseFlag {
			fmt.Fprintf(w, "filename regexp matched %d files\n", len(fnames))
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
					serveMatches(w, r, name, &g, stdre)
					r.Close()
					continue
				}
			}
			continue
		}
		serveMatches(w, file, name, &g, stdre)
		file.Close()
	}

	fmt.Fprintf(w, "\n%d matches in %.3fs\n", g.Matches, time.Since(start).Seconds())
	if g.Limited {
		fmt.Fprintf(w, "more matches not shown due to match limit\n")
	}
}

var homePage = `<!DOCTYPE html>
<html>
<head>
<link rel="stylesheet" type="text/css" href="_static/viewer.css" />
<body>
<p>
<form action="/" class="search-form">
<input type="text" name="q" value="QUERY" placeholder="pattern" class="search-form__input">
<button type="submit">submit</button>
</form>
<p>
<hr>
<pre>
`

func show(w http.ResponseWriter, r *http.Request) {
	file := strings.TrimPrefix(r.URL.Path, "/show")
	if strings.HasPrefix(file, "/") && filepath.IsAbs(file[1:]) {
		// Turn /c:/foo into c:/foo on Windows.
		file = file[1:]
	}
	// TODO maybe trim file by ix.roots
	// TODO zips
	info, err := os.Stat(file)
	if err != nil {
		// TODO
		http.Error(w, err.Error(), 500)
		return
	}
	if info.IsDir() {
		dirs, err := os.ReadDir(file)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		serveDir(w, file, dirs)
		return
	}

	data, err := os.ReadFile(file)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	qarg := r.FormValue("q")
	larg := r.FormValue("l")

	serveFile(w, file, data, qarg, larg)
}

func printHeader(w io.Writer, file string) {
	e := html.EscapeString
	fmt.Fprintf(w, "<!DOCTYPE html>\n<head>\n")
	fmt.Fprintf(w, "<link rel=\"stylesheet\" href=\"/_static/viewer.css\">\n")
	fmt.Fprintf(w, "<script src=\"/_static/viewer.js\"></script>\n")
	fmt.Fprintf(w, `<title>%s - code search</title>`, e(file))
	fmt.Fprintf(w, "\n</head><body onload=\"scrollToLine()\"><pre>\n")
	f := ""
	elems := strings.Split(file, "/")
	// NOTE: this fixes double // prefix in absolute paths
	if elems[0] == "" {
		elems = elems[1:]
	}
	for _, elem := range elems {
		f += "/" + elem
		fmt.Fprintf(w, `/<a href="/show%s">%s</a>`, e(f), e(elem))
	}
	fmt.Fprintf(w, `</b> <small>(<a href="/">index</a>)</small>`)
	fmt.Fprintf(w, "\n\n")
}

func serveDir(w io.Writer, file string, dir []fs.DirEntry) {
	e := html.EscapeString
	printHeader(w, file)
	for _, d := range dir {
		// Note: file is the full path including mod@vers.
		file := path.Join(file, d.Name())
		fmt.Fprintf(w, "<a href=\"/show%s\">%s</a>\n", e(file), e(path.Base(file)))
	}
}

var nl = []byte("\n")

func serveFile(w http.ResponseWriter, name string, data []byte, qarg, larg string) {
	start := time.Now()

	if !isText(data) {
		w.Write(data)
		fmt.Fprintf(w, "\n served in %.3fs\n", time.Since(start).Seconds())
		return
	}

	printHeader(w, name)

	var re *regexp.Regexp
	var stdre *stdregexp.Regexp
	if qarg != "" {
		re, stdre, _ = compileQuery(qarg)
	}

	selectedLine := -1
	if n, err := strconv.Atoi(larg); err == nil {
		selectedLine = n
	}

	lineCount := bytes.Count(data, nl) + 1
	wid := len(fmt.Sprintf("%d", lineCount))
	wid = (wid+2+7)&^7 - 2

	e := html.EscapeString

	for n := 1; len(data) > 0; n += 1 {
		var line []byte
		line, data, _ = bytes.Cut(data, nl)

		end := -1
		if re != nil {
			end = re.Match(line, n == 1, true)
		}

		if end == -1 {
			fmt.Fprintf(w, "<span id=\"L%d\">%*d  %s\n</span>",
				n, wid, n, e(string(line)))
			continue
		}

		maybeClass := ""
		if selectedLine == n {
			maybeClass = `class="sel"`
		}
		fmt.Fprintf(w, "<span id=\"L%d\"%s>%*d  %s\n</span>",
			n, maybeClass, wid, n, markLineMatches(line, stdre))
	}

	fmt.Fprintf(w, "\n served in %.3fs\n", time.Since(start).Seconds())
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
