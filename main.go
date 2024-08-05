package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/sirupsen/logrus"
	"github.com/xperimental/hugo-preview/internal/config"
	"github.com/xperimental/hugo-preview/internal/render"
	"github.com/xperimental/hugo-preview/internal/repository"
	"github.com/xperimental/hugo-preview/internal/server"
)

var (
	log = &logrus.Logger{
		Out: os.Stderr,
		Formatter: &logrus.TextFormatter{
			DisableTimestamp: true,
		},
		Hooks: logrus.LevelHooks{},
		Level: logrus.InfoLevel,
	}
)

func main() {
	cfg, err := config.GetConfig(os.Args)
	switch {
	case err == config.NoErrShowDefaults:
		log.Debug("Showing default configuration")
		if err := config.WriteConfig(os.Stdout, cfg); err != nil {
			log.Errorf("Error writing config: %s", err)
		}
		return
	case err != nil:
		log.Fatalf("Error in configuration: %s", err)
	default:
		log.SetLevel(cfg.LogLevel)
	}

	renderQueue := render.NewQueue(log.WithField("component", "render"), cfg)

	repo, err := repository.New(log.WithField("component", "repository"), cfg.Repository, renderQueue)
	if err != nil {
		log.Fatalf("Error creating repository: %s", err)
	}

	srv, err := server.New(log.WithField("component", "server"), cfg.Server, repo)
	if err != nil {
		log.Fatalf("Error creating server: %s", err)
	}

	if err := runMain(renderQueue, repo, srv); err != nil {
		log.Fatalln(err)
	}

	log.Infoln("Shutdown complete.")
}

func runMain(renderQueue *render.Queue, repo *repository.Repository, srv *server.Server) error {
	wg := &sync.WaitGroup{}
	ctx, cancel := initSignalHandler()
	defer cancel()

	renderQueue.Start(ctx, wg)

	if err := repo.Start(ctx, wg); err != nil {
		return fmt.Errorf("error starting repository: %s", err)
	}

	if err := srv.Start(ctx, wg); err != nil {
		return fmt.Errorf("error starting server: %s", err)
	}

	log.Infof("Startup complete.")
	wg.Wait()
	return nil
}

func initSignalHandler() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Debugf("Got signal: %v", sig)
		cancel()
		signal.Reset(syscall.SIGTERM, syscall.SIGINT)
	}()

	return ctx, cancel
}
