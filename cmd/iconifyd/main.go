package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/moddengine/iconifyd/pkg/iconifyapi"
	"github.com/moddengine/iconifyd/pkg/iconifypack"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: iconifyd pack <json-directory> <output.sqlite> | serve [-db path] [-listen :8080]")
	}
	switch args[0] {
	case "pack":
		return iconifypack.Run(args[1:])
	case "serve":
		flags := flag.NewFlagSet("serve", flag.ContinueOnError)
		database := flags.String("db", iconifyapi.DefaultDatabasePath, "SQLite database path")
		listen := flags.String("listen", ":8080", "HTTP listen address")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if flags.NArg() != 0 {
			return errors.New("unexpected serve arguments")
		}
		icons, err := iconifyapi.New(*database)
		if err != nil {
			return err
		}
		defer icons.Close()
		server := &http.Server{
			Addr: *listen, Handler: icons,
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		done := make(chan error, 1)
		go func() { done <- server.ListenAndServe() }()
		log.Printf("serving Iconify API on %s", *listen)
		select {
		case err := <-done:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdown); err != nil {
				server.Close()
				return err
			}
			return nil
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}
