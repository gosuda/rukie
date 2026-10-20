package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/gosuda/rukie/internal/ruk"
	gen "github.com/gosuda/rukie/internal/ruk/internal/rukgen"
	"github.com/gosuda/rukie/internal/ruk/wire"
)

func generate(mode string, raw []byte) error {
	wireSrc, sentinelsSrc, err := gen.Generate(raw)
	if err != nil {
		return err
	}
	switch mode {
	case "wire":
		fmt.Print(wireSrc)
	case "sentinels":
		fmt.Print(sentinelsSrc)
	default:
		return fmt.Errorf("rukgen: unknown mode %q", mode)
	}
	return nil
}

func fetch(socket, repo string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := ruk.Dial(ctx, socket)
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()
	doc, err := ruk.Schema(ctx, client, ruk.Repo{Path: repo}, wire.SchemaArgs{})
	if err != nil {
		return err
	}
	fmt.Println(string(doc))
	return nil
}

func main() {
	mode := flag.String("mode", "wire", "output set: wire or sentinels")
	fetchSocket := flag.String("fetch", "", "daemon socket for snapshot refresh")
	fetchRepo := flag.String("repo", "", "repository path for snapshot refresh")
	flag.Parse()
	if *fetchSocket != "" {
		if *fetchRepo == "" {
			fmt.Fprintln(os.Stderr, "rukgen: fetch needs -repo")
			os.Exit(1)
		}
		if err := fetch(*fetchSocket, *fetchRepo); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := generate(*mode, raw); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
