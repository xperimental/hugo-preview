package repository

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/xperimental/hugo-preview/internal/config"
	"github.com/xperimental/hugo-preview/internal/data"
	"github.com/xperimental/hugo-preview/internal/render"
)

var (
	hashRegex        = regexp.MustCompile("[0-9a-f]+")
	errShutdown      = errors.New("repository is shutting down")
	errBaseNotCloned = errors.New("base clone not done")
)

type Repository struct {
	log      config.Logger
	cfg      config.Repository
	renderer *render.Queue

	baseRepo         *git.Repository
	repoLock         *sync.RWMutex
	activeClones     map[string]*Clone
	renderStatusChan chan *render.Status
	shutdown         bool
}

func New(log config.Logger, cfg config.Repository, renderer *render.Queue) (*Repository, error) {
	if cfg.URL == "" {
		return nil, errors.New("URL can not be empty")
	}
	log.Infof("Repository URL: %s", cfg.URL)

	return &Repository{
		log:      log,
		cfg:      cfg,
		renderer: renderer,

		baseRepo:         nil,
		repoLock:         &sync.RWMutex{},
		activeClones:     make(map[string]*Clone),
		renderStatusChan: make(chan *render.Status),
		shutdown:         false,
	}, nil
}

func (r *Repository) Start(ctx context.Context, wg *sync.WaitGroup) error {
	wg.Add(1)

	go func() {
		defer wg.Done()

		r.log.Infoln("Cloning repository...")
		start := time.Now()
		baseRepo, err := git.Init(memory.NewStorage(), nil)
		if err != nil {
			r.log.Errorf("Error creating repository: %s", err)
			return
		}
		r.setBaseRepo(baseRepo)

		refSpecs := make([]gitconfig.RefSpec, len(r.cfg.RefSpecs))
		for i, r := range r.cfg.RefSpecs {
			refSpecs[i] = gitconfig.RefSpec(r)
		}
		if _, err := baseRepo.CreateRemote(&gitconfig.RemoteConfig{
			Name: "origin",
			URLs: []string{
				r.cfg.URL,
			},
			Fetch: refSpecs,
		}); err != nil {
			r.log.Errorf("Error creating remote: %s", err)
			return
		}

		if err := r.fetchBase(ctx); err != nil {
			r.log.Errorf("Error during initial fetch: %s", err)
			return
		}

		r.log.Infof("Initial clone successful in %s", time.Since(start))

		r.log.Debugf("Fetching repo every %s", r.cfg.FetchInterval)
		fetchTimer := time.NewTicker(r.cfg.FetchInterval)
		defer fetchTimer.Stop()

		for {
			select {
			case <-ctx.Done():
				r.shutdown = true

				r.log.Debugln("Shutting down repository...")
				r.cleanup()
				return
			case status := <-r.renderStatusChan:
				r.log.Debugf("Got render status for %s", status.CommitHash)

				if err := r.setCloneStatus(status); err != nil {
					r.log.Errorf("Error updating clone status for %q: %s", status.CommitHash, err)
				}
			case start := <-fetchTimer.C:
				r.log.Debugf("Starting fetch at %s", start.UTC())
				if err := r.fetchBase(ctx); err != nil {
					r.log.Errorf("Error during fetch: %s", err)
				}
				r.log.Debugf("Fetch done in %s", time.Since(start))
			}
		}
	}()

	return nil
}

func (r *Repository) ListBranches(ctx context.Context) (*data.BranchList, error) {
	if r.shutdown {
		return nil, errShutdown
	}

	if r.baseRepo == nil {
		return nil, errBaseNotCloned
	}

	iter, err := r.baseRepo.Branches()
	if err != nil {
		return nil, fmt.Errorf("failed to list branches: %w", err)
	}

	result := &data.BranchList{
		Branches: []data.Branch{},
	}
loop:
	for {
		b, err := iter.Next()
		switch {
		case err == io.EOF:
			break loop
		case err != nil:
			return nil, fmt.Errorf("error iterating branches: %w", err)
		default:
		}

		c, err := r.baseRepo.CommitObject(b.Hash())
		if err != nil {
			r.log.Errorf("Error getting commit %q for branch %q: %s", b.Hash(), b.Name(), err)
			continue
		}

		result.Branches = append(result.Branches, data.Branch{
			Name: b.Name().Short(),
			Commit: data.Commit{
				Hash:    b.Hash().String(),
				Message: c.Message,
				Committer: data.User{
					Name:  c.Committer.Name,
					Email: c.Committer.Email,
					Date:  c.Committer.When,
				},
				Author: data.User{
					Name:  c.Author.Name,
					Email: c.Author.Email,
					Date:  c.Author.When,
				},
			},
		})
	}
	return result, nil
}

func (r *Repository) SiteHandler(ctx context.Context, refName string) (http.Handler, error) {
	if r.shutdown {
		return nil, errShutdown
	}

	if r.baseRepo == nil {
		return nil, errBaseNotCloned
	}

	resolved, err := r.resolveRef(refName)
	if err != nil {
		return nil, fmt.Errorf("reference %q can not be resolved: %w", refName, err)
	}
	r.log.Debugf("Reference %q resolved to commit %q", refName, resolved)

	if resolved != refName {
		baseURL, err := r.renderer.BaseURL(resolved)
		if err != nil {
			return nil, fmt.Errorf("error creating base URL for resolved: %w", err)
		}

		return http.RedirectHandler(baseURL.String()+"/", http.StatusFound), nil
	}

	clone, err := r.getClone(ctx, resolved)
	if err != nil {
		return nil, err
	}

	return clone, nil
}

func (r *Repository) cleanup() {
	r.repoLock.Lock()
	defer r.repoLock.Unlock()

	for _, clone := range r.activeClones {
		r.log.Debugf("Cleaning up clone %q", clone.Commit)

		if err := r.doCleanupClone(clone); err != nil {
			r.log.Errorf("Error cleaning up clone: %s", err)
		}
	}
}

func (r *Repository) cleanupClone(commitHash string) error {
	r.repoLock.Lock()
	defer r.repoLock.Unlock()

	clone, ok := r.activeClones[commitHash]
	if !ok {
		return fmt.Errorf("clone not found: %s", commitHash)
	}

	if err := r.doCleanupClone(clone); err != nil {
		return err
	}

	delete(r.activeClones, commitHash)
	return nil
}

func (r *Repository) doCleanupClone(clone *Clone) error {
	if err := os.RemoveAll(clone.Directory); err != nil {
		return fmt.Errorf("error removing directory %q: %s", clone.Directory, err)
	}

	return nil
}

func (r *Repository) setCloneStatus(renderStatus *render.Status) error {
	r.repoLock.RLock()
	defer r.repoLock.RUnlock()

	clone, ok := r.activeClones[renderStatus.CommitHash]
	if !ok {
		return fmt.Errorf("clone not found: %s", renderStatus.CommitHash)
	}

	clone.RenderStatus = renderStatus
	return nil
}

func (r *Repository) setBaseRepo(baseRepo *git.Repository) {
	r.repoLock.Lock()
	defer r.repoLock.Unlock()

	r.baseRepo = baseRepo
}

func (r *Repository) fetchBase(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, r.cfg.FetchTimeout)
	defer cancel()

	err := r.baseRepo.FetchContext(ctx, &git.FetchOptions{})
	switch {
	case err == git.NoErrAlreadyUpToDate:
	case err != nil:
		return err
	default:
	}
	return nil
}

func (r *Repository) resolveRef(refName string) (string, error) {
	if refName == "head" || refName == "HEAD" {
		return "", errors.New("HEAD can not be used")
	}

	resolved, err := r.baseRepo.ResolveRevision(plumbing.Revision(refName))
	if err == nil {
		return resolved.String(), nil
	}

	if hashRegex.MatchString(refName) {
		commit, err := r.findCommit(refName)
		if err != nil {
			return "", fmt.Errorf("can not find commit %q: %s", refName, err)
		}

		return commit.String(), nil
	}

	return "", fmt.Errorf("unknown reference: %s", refName)
}

func (r *Repository) findCommit(commitHash string) (*plumbing.Hash, error) {
	cIter, err := r.baseRepo.CommitObjects()
	if err != nil {
		return nil, err
	}

	candidates := []*plumbing.Hash{}
loop:
	for {
		c, err := cIter.Next()
		switch {
		case err == io.EOF:
			break loop
		case err != nil:
			return nil, err
		default:
		}

		if strings.HasPrefix(c.Hash.String(), commitHash) {
			candidates = append(candidates, &c.Hash)
		}
	}

	num := len(candidates)
	if num == 0 {
		return nil, fmt.Errorf("commit not found: %s", commitHash)
	}

	if num > 1 {
		return nil, fmt.Errorf("commit %q is not unique. found %d candidates", commitHash, num)
	}

	return candidates[0], nil
}

func (r *Repository) getClone(ctx context.Context, commitHash string) (*Clone, error) {
	clone := r.lookupClone(commitHash)
	if clone == nil {
		clone, err := r.createClone(ctx, commitHash)
		if err != nil {
			return nil, fmt.Errorf("error creating clone for %q: %s", commitHash, err)
		}

		return clone, nil
	}

	r.log.Debugf("Reusing clone in %q for %q", clone.Directory, commitHash)
	clone.LastAccess = time.Now()
	return clone, nil
}

func (r *Repository) lookupClone(commitHash string) *Clone {
	r.repoLock.RLock()
	defer r.repoLock.RUnlock()

	return r.activeClones[commitHash]
}

func (r *Repository) createClone(ctx context.Context, commitHash string) (*Clone, error) {
	r.repoLock.Lock()
	defer r.repoLock.Unlock()

	if r.shutdown {
		return nil, errShutdown
	}

	if clone := r.activeClones[commitHash]; clone != nil {
		return clone, nil
	}

	r.log.Debugf("Creating clone for %q", commitHash)

	baseURL, err := r.renderer.BaseURL(commitHash)
	if err != nil {
		return nil, fmt.Errorf("can not create base URL: %s", err)
	}

	dir, err := ioutil.TempDir("", "hugo-preview-")
	if err != nil {
		return nil, fmt.Errorf("can not create clone directory: %s", err)
	}
	r.log.Debugf("Created directory %q", dir)

	clone := NewClone(r.log, commitHash, baseURL.Path, dir)

	renderInfo := &render.Info{
		RepositoryURL: r.cfg.URL,
		CommitHash:    commitHash,
		TargetPath:    dir,
		StatusChan:    r.renderStatusChan,
	}
	r.renderer.Submit(renderInfo)

	r.activeClones[commitHash] = clone
	return clone, nil
}
