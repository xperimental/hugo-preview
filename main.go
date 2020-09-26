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
	if err != nil {
		log.Fatalf("Error in configuration: %s", err)
	}
	log.SetLevel(cfg.LogLevel)

	repo, err := repository.New(log.WithField("component", "repository"), cfg.Repository)
	if err != nil {
		log.Fatalf("Error creating repository: %s", err)
	}

	srv, err := server.New(log.WithField("component", "server"), cfg.Server, repo)
	if err != nil {
		log.Fatalf("Error creating server: %s", err)
	}

	if err := runMain(repo, srv); err != nil {
		log.Fatalln(err)
	}

	log.Infoln("Shutdown complete.")
}

func runMain(repo *repository.Repository, srv *server.Server) error {
	wg := &sync.WaitGroup{}
	ctx, cancel := initSignalHandler()
	defer cancel()

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

	sigCh := make(chan os.Signal)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		sig := <-sigCh
		log.Debugf("Got signal: %v", sig)
		cancel()
		signal.Reset(syscall.SIGTERM, syscall.SIGINT)
	}()

	return ctx, cancel
}
