package render

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sirupsen/logrus"
	"github.com/xperimental/hugo-preview/internal/config"
)

type Status struct {
	Info  *Info
	Error error
}

type Info struct {
	RepositoryURL string
	CommitHash    string
	TargetPath    string
	StatusChan    chan<- *Status
}

// Queue implements a renderqueue for Hugo.
type Queue struct {
	log      config.Logger
	hugoPath string
	baseURL  string
	queue    chan *Info
}

func NewQueue(log config.Logger, cfg config.Config) *Queue {
	return &Queue{
		log:      log,
		hugoPath: cfg.HugoPath,
		baseURL:  cfg.Server.BaseURL,
		queue:    make(chan *Info, 1),
	}
}

func (q *Queue) Start(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer q.log.Debug("Render queue shut down.")

		q.log.Debug("Render queue ready.")
		for {
			select {
			case <-ctx.Done():
				return
			case info := <-q.queue:
				q.log.Debugf("Got render info: %v", info)

				err := q.renderSite(ctx, info)
				info.StatusChan <- &Status{
					Info:  info,
					Error: err,
				}
			}
		}
	}()
}

func (q *Queue) Submit(info *Info) {
	q.queue <- info
}

func (q *Queue) renderSite(ctx context.Context, info *Info) error {
	repo, err := git.PlainCloneContext(ctx, info.TargetPath, false, &git.CloneOptions{
		URL:               info.RepositoryURL,
		RecurseSubmodules: git.DefaultSubmoduleRecursionDepth,
	})
	if err != nil {
		return fmt.Errorf("can not clone repository: %w", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("can not get worktree: %w", err)
	}

	err = wt.Checkout(&git.CheckoutOptions{
		Hash: plumbing.NewHash(info.CommitHash),
	})
	if err != nil {
		return fmt.Errorf("error during checkout: %w", err)
	}

	baseURL, err := q.BaseURL(info.CommitHash)
	if err != nil {
		return fmt.Errorf("can not format base URL: %w", err)
	}
	q.log.Debugf("Base URL: %s", baseURL)

	hugoArguments := []string{
		"-b",
		baseURL.String(),
	}

	cmd := exec.Command(q.hugoPath, hugoArguments...)
	cmd.Dir = info.TargetPath
	cmd.Stdout = q.log.WriterLevel(logrus.DebugLevel)
	cmd.Stderr = q.log.WriterLevel(logrus.ErrorLevel)

	q.log.Debugf("Running command: %s %v", q.hugoPath, hugoArguments)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("can not start renderer: %w", err)
	}

	q.log.Debugln("Waiting for render to complete.")
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("error during execution of renderer: %w", err)
	}

	q.log.Debugln("Rendering done.")
	return nil
}

func (q *Queue) BaseURL(commitHash string) (*url.URL, error) {
	u, err := url.Parse(q.baseURL)
	if err != nil {
		return nil, fmt.Errorf("can not parse baseURL: %w", err)
	}

	u.Path = filepath.Join(u.Path, "preview", commitHash)
	return u, nil
}
