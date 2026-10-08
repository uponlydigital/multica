package execenv

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
)

// Background
//
// Hermes keeps its Python dependency environments per data root:
// `<root>/installs/<install key>` (hermes-agent pm/environments.py). A process
// whose HERMES_HOME sits outside ~/.hermes treats that HERMES_HOME as its own
// root, so the per-task overlay is a root of its own. When the overlay is seeded
// from a named profile (`-p <name>`), the profile dir has no `installs/` entry
// to mirror, and Hermes builds a complete environment inside the overlay before
// it can answer ACP — about 390 MB and 1.5-3 minutes on every task, thrown away
// with the task directory.
//
// This file gives the overlay one persistent installs store per (Multica
// daemon profile, Hermes source home):
//
//	<multica profile dir>/hermes-installs/<hermes profile>
//
// linked in as `<overlay>/installs`. The first task for that source home builds
// the environment once; later tasks find it current and start straight away.
// Hermes still sees a borrowed root (the store is not the owning install's
// state dir), so its #123238 protections hold: a task never rebinds the shared
// launchers or runs the owner's post-update tail.
//
// Keyed per source home rather than shared by all: the environment Hermes
// builds includes the source profile's enabled plugins, so two profiles with
// different plugin sets would otherwise rebuild over each other.
//
// A source home that already HAS an `installs/` entry (the native ~/.hermes
// root used without -p) keeps linking to it, which is what the generic mirror
// did before this change.
//
// The store is not garbage-collected by the daemon: it is shared by every task
// of the source home and must outlive any one of them. A Hermes update that
// changes its dependency set adds one new generation under the store; pruning
// old generations is left to Hermes and the operator.

// hermesInstallsEntry is the dependency-state directory Hermes resolves under
// its data root.
const hermesInstallsEntry = "installs"

// hermesInstallsStoreRoot is the directory under the daemon's Multica profile
// dir that holds the shared installs stores. Separate from hermes-state/ so the
// per-agent memory/session GC never walks it.
const hermesInstallsStoreRoot = "hermes-installs"

// HermesInstallsStorePath returns the shared installs store for
// (daemonProfile, sourceHome), or "" when the Multica profile dir cannot be
// resolved (the overlay then keeps Hermes' task-local build).
func HermesInstallsStorePath(daemonProfile, sourceHome string) string {
	profileDir, err := cli.ProfileDir(daemonProfile)
	if err != nil {
		return ""
	}
	return filepath.Join(profileDir, hermesInstallsStoreRoot, hermesMemoryProfileSegment(sourceHome))
}

// mountHermesInstalls links `<hermesHome>/installs` to the dependency state the
// task should share. Order of precedence:
//
//  1. the source home's own `installs/` directory, when it has one;
//  2. the shared store, when one is given (created on first use);
//  3. nothing: any stale link is removed and Hermes builds task-local, as it
//     did before this change.
//
// Idempotent across Reuse; a real directory left by an older daemon's
// task-local build is replaced by the link.
func mountHermesInstalls(hermesHome, sourceHome, store string, logger *slog.Logger) error {
	dst := filepath.Join(hermesHome, hermesInstallsEntry)
	shared := strings.TrimSpace(sourceHome)
	if shared == "" {
		shared = platformDefaultHermesHome()
	}

	target := ""
	if fi, err := os.Stat(filepath.Join(shared, hermesInstallsEntry)); err == nil && fi.IsDir() {
		target = filepath.Join(shared, hermesInstallsEntry)
	} else if store != "" {
		if err := os.MkdirAll(store, 0o700); err != nil {
			return fmt.Errorf("create hermes installs store %s: %w", store, err)
		}
		target = store
	}

	if target == "" {
		if fi, err := os.Lstat(dst); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(dst); err != nil {
				return fmt.Errorf("remove stale installs link: %w", err)
			}
		}
		return nil
	}
	if err := linkSharedHermesEntry(target, dst); err != nil {
		return fmt.Errorf("link installs: %w", err)
	}
	if logger != nil {
		logger.Debug("execenv: hermes installs linked", "target", target)
	}
	return nil
}

// linkHermesInstalls is mountHermesInstalls for the Prepare/Reuse call sites.
// Failure is not fatal: the task still runs and Hermes builds its environment
// task-locally, which is the behaviour before the shared store existed.
func linkHermesInstalls(hermesHome, sourceHome, store string, logger *slog.Logger) {
	if err := mountHermesInstalls(hermesHome, sourceHome, store, logger); err != nil && logger != nil {
		logger.Warn("execenv: link hermes installs failed; hermes will build task-local", "error", err)
	}
}
