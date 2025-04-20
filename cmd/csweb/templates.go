package main

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"strings"
)

type Templates interface {
	Render(w io.Writer, name string, data any) error
}

func initTemplate(fs fs.FS, patterns ...string) (*template.Template, error) {
	t := template.New("")

	// NOTE: must follow standard naming conventions; see
	// https://pkg.go.dev/text/template#hdr-Functions
	t.Funcs(template.FuncMap{
		"trimspace": strings.TrimSpace,
		"add":       func(lhs, rhs int) int { return lhs + rhs },
		"sub":       func(lhs, rhs int) int { return lhs - rhs },
	})

	return t.ParseFS(fs, patterns...)
}

type staticTemplates struct {
	template *template.Template
}

func (st *staticTemplates) Render(w io.Writer, name string, data any) error {
	return st.template.ExecuteTemplate(w, name, data)
}

func InitStaticTemplates(fs fs.FS, patterns ...string) (Templates, error) {
	t, err := initTemplate(fs, patterns...)
	if err != nil {
		return nil, err
	}
	return &staticTemplates{template: t}, nil
}

type dynamicTemplates struct {
	fs       fs.FS
	patterns []string
}

func (dt *dynamicTemplates) Render(w io.Writer, name string, data any) error {
	t, err := initTemplate(dt.fs, dt.patterns...)
	if err != nil {
		return fmt.Errorf("could not init template: %w", err)
	}
	return t.ExecuteTemplate(w, name, data)
}

func InitDynamicTemplates(fs fs.FS, patterns ...string) (Templates, error) {
	return &dynamicTemplates{fs: fs, patterns: patterns}, nil
}
