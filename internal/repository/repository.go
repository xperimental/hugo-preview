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
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/sirupsen/logrus"
	"github.com/xperimental/hugo-preview/internal/config"
)

var (
	hashRegex   = regexp.MustCompile("[0-9a-f]+")
	errShutdown = errors.New("repository is shutting down")
)

type Repository struct {
	log          config.Logger
	cfg          config.Repository
	baseRepo     *git.Repository
	repoLock     *sync.RWMutex
	activeClones map[string]*Clone
	shutdown     bool
}

func New(log config.Logger, cfg config.Repository) (*Repository, error) {
	if cfg.URL == "" {
		return nil, errors.New("URL can not be empty")
	}
	log.Infof("Repository URL: %s", cfg.URL)

	return &Repository{
		log:          log,
		cfg:          cfg,
		repoLock:     &sync.RWMutex{},
		activeClones: make(map[string]*Clone),
	}, nil
}

func (r *Repository) Start(ctx context.Context, wg *sync.WaitGroup) error {
	wg.Add(1)

	go func() {
		defer wg.Done()

		r.log.Infoln("Cloning repository...")
		start := time.Now()
		baseRepo, err := git.CloneContext(ctx, memory.NewStorage(), nil, &git.CloneOptions{
			URL: r.cfg.URL,
		})
		if err != nil {
			r.log.Errorf("Error cloning repository: %s", err)
			return
		}

		r.log.Infof("Initial clone successful in %s", time.Since(start))
		r.setBaseRepo(baseRepo)

		r.log.Debugf("Fetching repo every %s", r.cfg.FetchInterval)
		fetchTimer := time.NewTicker(r.cfg.FetchInterval)
		defer fetchTimer.Stop()

		for {
			select {
			case <-ctx.Done():
				r.shutdown = true

				r.log.Debugln("Shutting down fetcher...")
				r.cleanup()
				return
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

func (r *Repository) SiteHandler(ctx context.Context, refName string) (http.Handler, error) {
	if r.shutdown {
		return nil, errShutdown
	}

	if r.baseRepo == nil {
		return nil, errors.New("base clone not done")
	}

	resolved, err := r.resolveRef(refName)
	if err != nil {
		return nil, fmt.Errorf("reference %q can not be resolved: %w", refName, err)
	}
	r.log.Debugf("Reference %q resolved to commit %q", refName, resolved)

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

		if err := os.RemoveAll(clone.Directory); err != nil {
			r.log.Errorf("Error removing directory %q: %s", clone.Directory, err)
		}
	}
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

	cloneLog := r.log.WithFields(logrus.Fields{
		"component": "clone",
		"commit":    commitHash,
	})
	cloneLog.Debugln("Creating clone")

	dir, err := ioutil.TempDir("", "hugo-preview-")
	if err != nil {
		return nil, fmt.Errorf("can not create clone directory: %s", err)
	}
	cloneLog.Debugf("Created directory %q", dir)

	clone, err := NewClone(cloneLog, r.cfg, commitHash, dir)
	if err != nil {
		if err := os.RemoveAll(dir); err != nil {
			cloneLog.Errorf("Error removing directory %q: %s", dir, err)
		}

		return nil, err
	}

	r.activeClones[commitHash] = clone
	return clone, nil
}
