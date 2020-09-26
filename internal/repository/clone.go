package repository

import (
	"fmt"
	"net/http"
	"path/filepath"
	"time"

	"github.com/xperimental/hugo-preview/internal/config"
	"github.com/xperimental/hugo-preview/internal/render"
)

const (
	defaultPublicPath = "public"
)

type Clone struct {
	Commit    string
	BasePath  string
	Directory string

	LastAccess   time.Time
	RenderStatus *render.Status
	handler      http.Handler
}

func NewClone(log config.Logger, commitHash, basePath, targetDir string) *Clone {
	fs := http.Dir(filepath.Join(targetDir, defaultPublicPath))
	handler := http.StripPrefix(basePath, logHandler(log, http.FileServer(fs)))

	return &Clone{
		Commit:    commitHash,
		BasePath:  basePath,
		Directory: targetDir,

		LastAccess:   time.Now(),
		RenderStatus: nil,
		handler:      handler,
	}
}

func (c *Clone) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if c.RenderStatus == nil {
		http.Error(w, "Clone not ready yet.", http.StatusInternalServerError)
		return
	}

	if c.RenderStatus.Error != nil {
		http.Error(w, fmt.Sprintf("Error during render: %s\n\nOutput:\n%s", c.RenderStatus.Error, c.RenderStatus.Output), http.StatusInternalServerError)
		return
	}

	c.handler.ServeHTTP(w, r)
}

func logHandler(log config.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Debugf("[%s] %s", r.Method, r.URL)

		next.ServeHTTP(w, r)
	})
}
