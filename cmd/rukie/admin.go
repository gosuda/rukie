package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"

	"github.com/gosuda/rukie/internal/config"
	"github.com/gosuda/rukie/internal/store"
	"github.com/gosuda/rukie/internal/sysops"
)

const (
	exitOK           = 0
	exitRuntime      = 1
	exitUsage        = 2
	exitOfflineGuard = 3
)

func adminExit(err error) int {
	switch {
	case err == nil:
		return exitOK
	case sysops.IsDowngrade(err):
		return sysops.ExitDowngradeRefused
	case isLiveGuard(err):
		return exitOfflineGuard
	default:
		return exitRuntime
	}
}

func isLiveGuard(err error) bool { return errors.Is(err, sysops.ErrDataDirLive) }

func emitJSON(v any) int {
	raw, err := jsonv2.Marshal(v, jsonv1.DefaultOptionsV1())
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err.Error())
		return exitRuntime
	}
	fmt.Println(string(raw))
	return exitOK
}

func loadAdminConfig(flagValue string) (config.Config, string, error) {
	path := configPath(flagValue)
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, path, err
	}
	return cfg, path, nil
}

var handWrittenAdminLeaves = map[string]bool{
	"secret-gen": true,
	"migrate":    true,
	"status":     true,
	"reconcile":  true,
	"ssh-keygen": true,
}

func tryHandWrittenAdmin(argv []string) (int, bool) {
	if len(argv) == 0 || !handWrittenAdminLeaves[argv[0]] {
		return 0, false
	}
	var code int
	switch argv[0] {
	case "secret-gen":
		code = runSecretGen(argv[1:])
	case "ssh-keygen":
		code = runSSHKeygen(argv[1:])
	case "migrate":
		code = runMigrate(argv[1:])
	case "status":
		code = runStatus(argv[1:])
	case "reconcile":
		code = runReconcile(argv[1:])
	}
	return code, true
}

func runSecretGen(argv []string) int {
	fs := flag.NewFlagSet("secret-gen", flag.ContinueOnError)
	out := fs.String("out", sysops.DefaultSecretKeyPath, "output key file")
	force := fs.Bool("force", false, "overwrite an existing key file")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	var (
		id  string
		err error
	)
	if *force {
		id, err = sysops.RotateSecretKey(*out)
	} else {
		id, err = sysops.GenerateSecretKey(*out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin secret-gen:", err.Error())
		return exitRuntime
	}
	if *asJSON {
		return emitJSON(map[string]string{"path": *out, "key_id": id})
	}
	fmt.Printf("wrote %s (key id %s)\n", *out, id)
	fmt.Fprintln(os.Stderr, "back up the key offline: rukie backup never copies it")
	return exitOK
}

func runSSHKeygen(argv []string) int {
	fs := flag.NewFlagSet("ssh-keygen", flag.ContinueOnError)
	out := fs.String("out", "", "output host key file")
	force := fs.Bool("force", false, "overwrite an existing key file")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "admin ssh-keygen: --out is required")
		return exitUsage
	}
	var (
		id  string
		err error
	)
	if *force {
		id, err = rotateSSHHostKey(*out)
	} else {
		id, err = generateSSHHostKey(*out)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin ssh-keygen:", err.Error())
		return exitRuntime
	}
	if *asJSON {
		return emitJSON(map[string]string{"path": *out, "fingerprint": id})
	}
	fmt.Printf("wrote %s (%s)\n", *out, id)
	return exitOK
}

func runMigrate(argv []string) int {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	configFlag := fs.String("config", "", "config file path")
	dryRun := fs.Bool("dry-run", false, "list pending versions without applying")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	cfg, _, err := loadAdminConfig(*configFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin migrate:", err.Error())
		return exitUsage
	}
	if *dryRun {
		pending, derr := sysops.PendingMigrations(cfg)
		if derr != nil {
			fmt.Fprintln(os.Stderr, "admin migrate:", derr.Error())
			return adminExit(derr)
		}
		res := pending
		if *asJSON {
			return emitJSON(map[string]any{"pending": res.Pending, "applied": res.AppliedMax, "code": res.CodeMax})
		}
		if len(res.Pending) == 0 {
			fmt.Printf("schema %d is current\n", res.CodeMax)
			return exitOK
		}
		fmt.Printf("pending: %v (applied %d, code %d)\n", res.Pending, res.AppliedMax, res.CodeMax)
		return exitOK
	}
	res, err := sysops.Migrate(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin migrate:", err.Error())
		return adminExit(err)
	}
	if *asJSON {
		return emitJSON(map[string]any{"applied": res.Applied, "schema": res.AppliedMax})
	}
	if res.Applied {
		fmt.Printf("migrated to schema %d\n", res.AppliedMax)
		return exitOK
	}
	fmt.Printf("schema %d is current\n", res.AppliedMax)
	return exitOK
}

func runStatus(argv []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	configFlag := fs.String("config", "", "config file path")
	asJSON := fs.Bool("json", false, "machine output")
	expectLive := fs.Bool("expect-live", false, "exit non-zero unless a server holds the data_dir lock")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	cfg, _, err := loadAdminConfig(*configFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin status:", err.Error())
		return exitUsage
	}
	rep, err := sysops.Status(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin status:", err.Error())
		return adminExit(err)
	}
	live := sysops.IsDataDirLive(cfg.DataDir)
	daemon := "stopped"
	if live {
		daemon = "live"
	}
	if *expectLive && !live {
		if *asJSON {
			_ = emitJSON(map[string]any{"daemon": daemon, "data_dir": rep.DataDir})
		} else {
			fmt.Printf("daemon: %s (no server holds %s)\n", daemon, cfg.DataDir)
		}
		return exitRuntime
	}
	if *asJSON {
		return emitJSON(map[string]any{
			"schema_applied": rep.SchemaApplied,
			"schema_code":    rep.SchemaCode,
			"downgrade":      rep.Downgrade,
			"store_readable": rep.StoreReadable,
			"data_dir":       rep.DataDir,
			"last_backup_at": rep.LastBackupAt,
			"backup_count":   rep.BackupCount,
			"repo_entries":   rep.RepoEntries,
			"daemon":         daemon,
		})
	}
	fmt.Printf("schema: applied %d, code %d", rep.SchemaApplied, rep.SchemaCode)
	if rep.Downgrade {
		fmt.Print(" (DOWNGRADE REFUSED)")
	}
	fmt.Printf("\nstore readable: %v\ndata dir: %s\nbackups: %d", rep.StoreReadable, rep.DataDir, rep.BackupCount)
	if rep.BackupCount > 0 {
		fmt.Printf(" (last %d)", rep.LastBackupAt)
	}
	fmt.Printf("\nrepo entries: %d\ndaemon: %s\n", rep.RepoEntries, daemon)
	return exitOK
}

func runReconcile(argv []string) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	configFlag := fs.String("config", "", "config file path")
	fromManifest := fs.String("from-manifest", "", "backup dir holding manifest.toml")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	cfg, _, err := loadAdminConfig(*configFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin reconcile:", err.Error())
		return exitUsage
	}
	rep, err := sysops.Reconcile(context.Background(), cfg, *fromManifest)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin reconcile:", err.Error())
		return adminExit(err)
	}
	if *asJSON {
		return emitJSON(map[string]any{
			"checked":    rep.Checked,
			"orphaned":   rep.Orphaned,
			"created":    rep.Created,
			"mismatched": rep.Mismatched,
		})
	}
	fmt.Printf("checked %d repo files: %d orphaned, %d created, %d mismatched\n",
		rep.Checked, len(rep.Orphaned), len(rep.Created), len(rep.Mismatched))
	for _, o := range rep.Orphaned {
		fmt.Printf("orphaned: %s\n", o)
	}
	for _, c := range rep.Created {
		fmt.Printf("created: %s\n", c)
	}
	for _, m := range rep.Mismatched {
		fmt.Printf("mismatched: %s\n", m)
	}
	return exitOK
}

func runBackup(argv []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	configFlag := fs.String("config", "", "config file path")
	out := fs.String("out", "", "backup base dir (default <data_dir>/backups)")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	cfg, _, err := loadAdminConfig(*configFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err.Error())
		return exitUsage
	}
	if derr := sysops.CheckDowngrade(cfg); derr != nil {
		fmt.Fprintln(os.Stderr, "backup:", derr.Error())
		return adminExit(derr)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, store.Options{Path: cfg.Store.SQLite.Path})
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup:", err.Error())
		return adminExit(err)
	}
	defer func() { _ = st.Close(ctx) }()
	base := *out
	if base == "" {
		base = filepath.Join(cfg.DataDir, "backups")
	}
	manifest, berr := sysops.Backup(ctx, st, cfg, base)
	if berr != nil {
		fmt.Fprintln(os.Stderr, "backup:", berr.Error())
		return adminExit(berr)
	}
	doc, _ := sysops.ReadManifest(filepath.Dir(manifest))
	if *asJSON {
		return emitJSON(map[string]any{"manifest": manifest, "key_id": doc.KeyID, "files": len(doc.Files)})
	}
	fmt.Printf("backup %s (%d files, key id %s)\n", manifest, len(doc.Files), doc.KeyID)
	return exitOK
}

func hoistFlags(argv []string, flagsWithSeparateValue []string, flagsWithoutValue []string) []string {
	isValued := map[string]bool{}
	for _, name := range flagsWithSeparateValue {
		isValued[name] = true
	}
	isBool := map[string]bool{}
	for _, name := range flagsWithoutValue {
		isBool[name] = true
	}
	var flags, rest []string
	i := 0
	for i < len(argv) {
		a := argv[i]
		name, inline, isFlag := cutHoistFlag(a)
		switch {
		case isFlag && isValued[name] && inline == "":
			flags = append(flags, a)
			if i+1 < len(argv) {
				i++
				flags = append(flags, argv[i])
			}
		case isFlag && (isValued[name] || isBool[name]):
			flags = append(flags, a)
		default:
			rest = append(rest, a)
		}
		i++
	}
	return append(flags, rest...)
}

func cutHoistFlag(arg string) (name, inline string, isFlag bool) {
	rest, ok := strings.CutPrefix(arg, "--")
	if !ok {
		return "", "", false
	}
	name, inline, _ = strings.Cut(rest, "=")
	return name, inline, true
}

func runRestore(argv []string) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	configFlag := fs.String("config", "", "config file path")
	force := fs.Bool("force", false, "proceed past hash warnings (never past a key mismatch)")
	asJSON := fs.Bool("json", false, "machine output")
	if err := fs.Parse(hoistFlags(argv, []string{"config"}, []string{"force", "json"})); err != nil {
		return exitUsage
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(os.Stderr, "usage: rukie restore <dir> [--force]")
		return exitUsage
	}
	cfg, _, err := loadAdminConfig(*configFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "restore:", err.Error())
		return exitUsage
	}
	ctx := context.Background()
	var rep sysops.RestoreReport
	var rerr error
	if *force {
		rep, rerr = sysops.RestoreLenient(ctx, cfg, rest[0])
	} else {
		rep, rerr = sysops.Restore(ctx, cfg, rest[0])
	}
	if rerr != nil {
		fmt.Fprintln(os.Stderr, "restore:", rerr.Error())
		return adminExit(rerr)
	}
	if *asJSON {
		return emitJSON(map[string]any{"files": rep.Files, "untracked": rep.Untracked, "warnings": rep.Warnings})
	}
	fmt.Printf("restored %d files", rep.Files)
	if len(rep.Untracked) > 0 {
		fmt.Printf(" (%d untracked post-boundary entries kept for reconcile)", len(rep.Untracked))
	}
	fmt.Println()
	for _, warning := range rep.Warnings {
		fmt.Fprintf(os.Stderr, "warning: restore --force proceeded past: %s\n", warning)
	}
	return exitOK
}
