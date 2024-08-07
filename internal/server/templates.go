package server

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"time"

	"github.com/dustin/go-humanize"
)

var (
	templateFuncMap = map[string]interface{}{
		"ago": func(date time.Time) string {
			return humanize.Time(date)
		},
	}

	//go:embed _templates
	templateFs embed.FS
)

func loadTemplates() (*template.Template, error) {
	subFs, err := fs.Sub(templateFs, "_templates")
	if err != nil {
		return nil, fmt.Errorf("can not load subdirectory: %w", err)
	}

	tpl, err := template.New("templates").Funcs(templateFuncMap).ParseFS(subFs, "*.html")
	if err != nil {
		return nil, fmt.Errorf("can not load templates: %w", err)
	}

	return tpl, nil
}
