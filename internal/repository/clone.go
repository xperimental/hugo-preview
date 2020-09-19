package repository

import (
	"bytes"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"text/template"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sirupsen/logrus"
	"github.com/xperimental/hugo-preview/internal/config"
)

type Clone struct {
	Log         config.Logger
	Commit      string
	Config      config.Repository
	LastAccess  time.Time
	Directory   string
	Rendered    bool
	RenderError error
}

func NewClone(log config.Logger, cfg config.Repository, commitHash, targetDir string) (*Clone, error) {
	return &Clone{
		Log:        log,
		Commit:     commitHash,
		Config:     cfg,
		LastAccess: time.Now(),
		Directory:  targetDir,
	}, nil
}

func (c *Clone) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !c.Rendered {
		done := c.startRender(r)

		select {
		case <-done:
			c.Log.Debugln("Render was done during request.")
		case <-time.After(30 * time.Second):
			c.Log.Debugln("Timed out waiting for render.")
		}
	}

	if c.RenderError != nil {
		http.Error(w, fmt.Sprintf("Error during render: %s", c.RenderError), http.StatusInternalServerError)
		return
	}

	fs := http.Dir(filepath.Join(c.Directory, c.Config.Renderer.ServeDir))
	http.FileServer(fs).ServeHTTP(w, r)
}

func (c *Clone) setRenderStatus(err error) {
	c.Rendered = true
	c.RenderError = err
}

func (c *Clone) startRender(r *http.Request) <-chan struct{} {
	doneCh := make(chan struct{})
	go func() {
		defer close(doneCh)

		repo, err := git.PlainClone(c.Directory, false, &git.CloneOptions{
			URL:               c.Config.URL,
			RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
		})
		if err != nil {
			c.setRenderStatus(fmt.Errorf("can not clone repository: %w", err))
			return
		}

		wt, err := repo.Worktree()
		if err != nil {
			c.setRenderStatus(fmt.Errorf("can not get worktree: %w", err))
			return
		}

		err = wt.Checkout(&git.CheckoutOptions{
			Hash: plumbing.NewHash(c.Commit),
		})
		if err != nil {
			c.setRenderStatus(fmt.Errorf("error during checkout: %w", err))
			return
		}

		command, err := c.generateCommand(r)
		if err != nil {
			c.setRenderStatus(fmt.Errorf("can not generate command: %w", err))
			return
		}
		c.Log.Debugf("Render command: %s", command)

		cmd := exec.Command(command[0], command[1:]...)
		cmd.Dir = c.Directory
		cmd.Stdout = c.Log.WriterLevel(logrus.DebugLevel)
		cmd.Stderr = c.Log.WriterLevel(logrus.ErrorLevel)

		if err := cmd.Start(); err != nil {
			c.setRenderStatus(fmt.Errorf("can not start renderer: %w", err))
			return
		}

		c.Log.Debugln("Waiting for render to complete.")
		if err := cmd.Wait(); err != nil {
			c.setRenderStatus(fmt.Errorf("error during execution of renderer: %w", err))
			return
		}

		c.Log.Debugln("Rendering done.")
		c.setRenderStatus(nil)
	}()

	return doneCh
}

func (c *Clone) generateCommand(r *http.Request) ([]string, error) {
	command := []string{}
	for _, tplString := range c.Config.Renderer.CommandTemplate {
		tpl, err := template.New("clone").Parse(tplString)
		if err != nil {
			return nil, fmt.Errorf("can not parse template: %w", err)
		}

		ctx := struct {
			Clone   *Clone
			Request *http.Request
		}{
			Clone:   c,
			Request: r,
		}

		buf := &bytes.Buffer{}
		if err := tpl.Execute(buf, ctx); err != nil {
			return nil, fmt.Errorf("can not execute template: %s", err)
		}

		command = append(command, buf.String())
	}

	return command, nil
}
