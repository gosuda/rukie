package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	gen "github.com/gosuda/rukie/internal/ruk/internal/rukgen"
)

func runModel(ctx context.Context, bin string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, "model", "--format", "json")
	cmd.Env = os.Environ()
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("regen: ruk model: %w", err)
	}
	return raw, nil
}

func writeFile(path string, raw []byte) error {
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("regen: write %s: %w", path, err)
	}
	return nil
}

func main() {
	os.Exit(run())
}

func run() int {
	bin := flag.String("bin", "", "ruk binary that serves the model document")
	modelOut := flag.String("model", "", "destination for ruk-model.json")
	wireOut := flag.String("wire", "", "destination for the generated wire sets")
	sentinelsOut := flag.String("sentinels", "", "destination for the generated sentinels")
	flag.Parse()
	if *bin == "" || *modelOut == "" || *wireOut == "" || *sentinelsOut == "" {
		fmt.Fprintln(os.Stderr, "regen: -bin, -model, -wire, and -sentinels are all required")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	raw, err := runModel(ctx, *bin)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	wireSrc, sentinelsSrc, err := gen.Generate(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = writeFile(*modelOut, raw); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = writeFile(*wireOut, []byte(wireSrc)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err = writeFile(*sentinelsOut, []byte(sentinelsSrc)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
